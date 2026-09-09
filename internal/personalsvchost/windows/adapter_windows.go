// Copyright (c) 2026 OceanBase.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package windows

import (
	"bytes"
	"context"
	"encoding/csv"
	json "encoding/json/v2"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
	"github.com/ob-labs/powercontext-go/internal/transportpolicy"
)

const (
	testTaskNamePrefix = `\PowerContext\Tests\`
	maxProbeBytes      = 4 << 10
	taskXMLNamespace   = "http://schemas.microsoft.com/windows/2004/02/mit/task"
	stopTimeout        = 15 * time.Second
	stopPollInterval   = 50 * time.Millisecond
)

var requestIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// Error is a typed, redacted Windows personal-service failure.
type Error struct {
	operation string
	cause     error
}

func (e *Error) Error() string {
	return fmt.Sprintf("Windows personal service %s failed", e.operation)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type taskScheduler interface {
	Query(context.Context) (personalsvc.TaskSchedulerState, []byte, error)
	Create(context.Context, string) error
	Run(context.Context) error
	End(context.Context) error
	Enable(context.Context) error
	Disable(context.Context) error
	Delete(context.Context) error
	State(context.Context) (personalsvc.ManagerState, error)
}

type artifactStore interface {
	Read(context.Context, string) ([]byte, bool, error)
	Write(context.Context, string, []byte) error
	Remove(context.Context, string) error
	RegularFile(context.Context, string) (bool, error)
}

// Adapter composes the portable Task Scheduler document with Windows-owned
// process, filesystem, identity, and probe boundaries.
type Adapter struct {
	plan         personalsvc.TaskSchedulerSpec
	artifactPath string
	taskName     string
	userSID      string
	userAccount  string
	userName     string
	userLookup   func(string) bool
	scheduler    taskScheduler
	artifacts    artifactStore
	httpClient   *http.Client
}

var _ personalsvc.Adapter = (*Adapter)(nil)

func newAdapter(
	plan personalsvc.TaskSchedulerSpec,
	userDataRoot string,
	taskName string,
	identity userIdentity,
	scheduler taskScheduler,
	artifacts artifactStore,
	httpClient *http.Client,
) (*Adapter, error) {
	resolvedRoot, rootErr := resolveUserDataRoot(userDataRoot)
	if scheduler == nil || artifacts == nil || httpClient == nil ||
		rootErr != nil || !validTaskName(taskName) || !validUserIdentity(identity) {
		return nil, newError("configuration", nil)
	}
	if _, err := plan.XML(); err != nil {
		return nil, newError("configuration", nil)
	}
	return &Adapter{
		plan:         plan,
		artifactPath: filepath.Join(resolvedRoot, filepath.FromSlash("PowerContext/Services/personal-server.xml")),
		taskName:     taskName,
		userSID:      identity.sid,
		userAccount:  identity.account,
		userName:     identity.name,
		userLookup:   identity.lookup,
		scheduler:    scheduler,
		artifacts:    artifacts,
		httpClient:   httpClient,
	}, nil
}

// Support reports whether the current user's Task Scheduler can resolve the
// exact configured task identity.
func (a *Adapter) Support(ctx context.Context) (personalsvc.Support, error) {
	if err := a.available(ctx); err != nil {
		return personalsvc.SupportUnsupported, err
	}
	_, _, err := a.scheduler.Query(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return personalsvc.SupportUnsupported, newError("support", ctx.Err())
		}
		return personalsvc.SupportUnsupported, nil
	}
	return personalsvc.SupportSupported, nil
}

// InspectArtifact verifies the exact stored XML through the portable parser.
func (a *Adapter) InspectArtifact(ctx context.Context) (personalsvc.Artifact, error) {
	if err := a.available(ctx); err != nil {
		return personalsvc.UnknownArtifact(), err
	}
	document, exists, err := a.artifacts.Read(ctx, a.artifactPath)
	if err != nil {
		return personalsvc.UnknownArtifact(), newError("inspect artifact", contextCause(ctx, err))
	}
	if !exists {
		return personalsvc.NoArtifact(), nil
	}
	plan, ok := a.parse(document)
	if !ok {
		return personalsvc.InvalidArtifact(), nil
	}
	registration := plan.Registration()
	regular, err := a.artifacts.RegularFile(ctx, registration.Definition().Binary())
	if err != nil {
		return personalsvc.UnknownArtifact(), newError("inspect artifact", contextCause(ctx, err))
	}
	if !regular {
		return personalsvc.InstalledArtifactWithDefinition(
			registration,
			personalsvc.DefinitionMissingExecutable,
		).WithRestoreSnapshot(plan), nil
	}
	return personalsvc.InstalledArtifact(registration).WithRestoreSnapshot(plan), nil
}

// InspectManager verifies independently queried Task Scheduler XML before
// treating the loaded object as PowerContext-owned.
func (a *Adapter) InspectManager(ctx context.Context) (personalsvc.ManagerRegistration, error) {
	if err := a.available(ctx); err != nil {
		return personalsvc.UnknownManager(), err
	}
	state, document, err := a.scheduler.Query(ctx)
	if err != nil {
		return personalsvc.UnknownManager(), newError("inspect manager", contextCause(ctx, err))
	}
	if state == personalsvc.TaskSchedulerAbsent {
		return personalsvc.NotLoadedManager(), nil
	}
	if state != personalsvc.TaskSchedulerPresent {
		return personalsvc.UnknownManager(), nil
	}
	plan, ok := a.parse(document)
	if !ok {
		return personalsvc.ForeignManager(), nil
	}
	return personalsvc.OwnedManager(plan.Registration()).WithRestoreSnapshot(plan), nil
}

// Probe accepts only the exact unauthenticated loopback liveness contract.
func (a *Adapter) Probe(ctx context.Context, endpoint string) (personalsvc.ProbeState, error) {
	if err := a.available(ctx); err != nil {
		return personalsvc.ProbeUnreachable, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.User != nil || !transportpolicy.IsLoopbackHost(parsed.Hostname()) {
		return personalsvc.ProbeUnreachable, newError("probe", nil)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/health/live", nil)
	if err != nil {
		return personalsvc.ProbeUnreachable, newError("probe", nil)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "powercontext-personal-service")
	response, err := a.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return personalsvc.ProbeUnreachable, newError("probe", ctx.Err())
		}
		return personalsvc.ProbeUnreachable, nil
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxProbeBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return personalsvc.ProbeUnreachable, newError("probe", ctx.Err())
		}
		return personalsvc.ProbeConflict, nil
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if len(body) > maxProbeBytes || response.StatusCode != http.StatusOK || mediaErr != nil ||
		mediaType != "application/json" || !requestIDPattern.MatchString(response.Header.Get("X-PowerContext-Request-ID")) {
		return personalsvc.ProbeConflict, nil
	}
	var health map[string]string
	if err := json.Unmarshal(body, &health); err != nil || len(health) != 1 || health["status"] != "ok" {
		return personalsvc.ProbeConflict, nil
	}
	return personalsvc.ProbeLive, nil
}

// Write atomically delegates the exact resolved XML artifact after fresh
// artifact and manager ownership checks.
func (a *Adapter) Write(ctx context.Context, registration personalsvc.Registration) error {
	if registration != a.plan.Registration() {
		return newError("write", nil)
	}
	artifact, manager, err := a.mutableState(ctx)
	if err != nil {
		return err
	}
	if artifact.State() != personalsvc.RegistrationInstalled && artifact.State() != personalsvc.RegistrationNotInstalled {
		return newError("write", nil)
	}
	if manager.Ownership() != personalsvc.ManagerOwnershipOwned && manager.Ownership() != personalsvc.ManagerOwnershipNotLoaded {
		return newError("write", nil)
	}
	document, err := a.renderPlan(a.plan)
	if err != nil {
		return err
	}
	if err := a.artifacts.Write(ctx, a.artifactPath, document); err != nil {
		return newError("write", contextCause(ctx, err))
	}
	return nil
}

// Reload is a no-op because schtasks /Create consumes the complete artifact.
func (a *Adapter) Reload(ctx context.Context) error { return a.available(ctx) }

// Enable creates or replaces only an absent or already-owned configured task.
func (a *Adapter) Enable(ctx context.Context) error {
	artifact, manager, err := a.mutableState(ctx)
	if err != nil {
		return err
	}
	if artifact.State() != personalsvc.RegistrationInstalled ||
		(manager.Ownership() != personalsvc.ManagerOwnershipOwned && manager.Ownership() != personalsvc.ManagerOwnershipNotLoaded) {
		return newError("enable", nil)
	}
	if err := a.scheduler.Create(ctx, a.artifactPath); err != nil {
		return newError("enable", contextCause(ctx, err))
	}
	if err := a.scheduler.Enable(ctx); err != nil {
		return newError("enable", contextCause(ctx, err))
	}
	return nil
}

// Start runs only a freshly verified owned task.
func (a *Adapter) Start(ctx context.Context) error {
	manager, err := a.InspectManager(ctx)
	if err != nil || manager.Ownership() != personalsvc.ManagerOwnershipOwned {
		return newError("start", contextCause(ctx, err))
	}
	if err := a.scheduler.Run(ctx); err != nil {
		return newError("start", contextCause(ctx, err))
	}
	return nil
}

// Stop ends only a freshly verified owned task. An absent task is stopped.
func (a *Adapter) Stop(ctx context.Context) error {
	manager, err := a.InspectManager(ctx)
	if err != nil {
		return err
	}
	if manager.Ownership() == personalsvc.ManagerOwnershipNotLoaded {
		return nil
	}
	if manager.Ownership() != personalsvc.ManagerOwnershipOwned {
		return newError("stop", nil)
	}
	state, err := a.scheduler.State(ctx)
	if err != nil {
		return newError("stop", contextCause(ctx, err))
	}
	if state == personalsvc.ManagerInactive || state == personalsvc.ManagerFailed {
		return nil
	}
	if state != personalsvc.ManagerActive {
		return newError("stop", nil)
	}
	if err := a.scheduler.End(ctx); err != nil {
		return newError("stop", contextCause(ctx, err))
	}
	return a.waitForStopped(ctx)
}

func (a *Adapter) waitForStopped(ctx context.Context) error {
	stopCtx, cancel := context.WithTimeout(ctx, stopTimeout)
	defer cancel()
	ticker := time.NewTicker(stopPollInterval)
	defer ticker.Stop()
	for {
		state, err := a.scheduler.State(stopCtx)
		if err != nil {
			return newError("stop", contextCause(stopCtx, err))
		}
		switch state {
		case personalsvc.ManagerInactive, personalsvc.ManagerFailed:
			return nil
		case personalsvc.ManagerActive:
		default:
			return newError("stop", nil)
		}
		select {
		case <-stopCtx.Done():
			return newError("stop", context.Cause(stopCtx))
		case <-ticker.C:
		}
	}
}

// Disable disables only a freshly verified owned task. An absent task is
// already disabled.
func (a *Adapter) Disable(ctx context.Context) error {
	manager, err := a.InspectManager(ctx)
	if err != nil {
		return err
	}
	if manager.Ownership() == personalsvc.ManagerOwnershipNotLoaded {
		return nil
	}
	if manager.Ownership() != personalsvc.ManagerOwnershipOwned {
		return newError("disable", nil)
	}
	if err := a.scheduler.Disable(ctx); err != nil {
		return newError("disable", contextCause(ctx, err))
	}
	return nil
}

// Remove deletes only an owned loaded task and its separately verified owned
// artifact.
func (a *Adapter) Remove(ctx context.Context) error {
	artifact, manager, err := a.mutableState(ctx)
	if err != nil {
		return err
	}
	if artifact.State() == personalsvc.RegistrationNotInstalled && manager.Ownership() == personalsvc.ManagerOwnershipNotLoaded {
		return nil
	}
	if artifact.State() != personalsvc.RegistrationInstalled ||
		(manager.Ownership() != personalsvc.ManagerOwnershipOwned && manager.Ownership() != personalsvc.ManagerOwnershipNotLoaded) {
		return newError("remove", nil)
	}
	if manager.Ownership() == personalsvc.ManagerOwnershipOwned {
		if err := a.scheduler.Delete(ctx); err != nil {
			return newError("remove", contextCause(ctx, err))
		}
	}
	if err := a.artifacts.Remove(ctx, a.artifactPath); err != nil {
		return newError("remove", contextCause(ctx, err))
	}
	return nil
}

// ManagerState reads state only after ownership has been freshly verified.
func (a *Adapter) ManagerState(ctx context.Context) (personalsvc.ManagerState, error) {
	manager, err := a.InspectManager(ctx)
	if err != nil {
		return personalsvc.ManagerUnknown, err
	}
	if manager.Ownership() != personalsvc.ManagerOwnershipOwned {
		return personalsvc.ManagerUnknown, nil
	}
	state, err := a.scheduler.State(ctx)
	if err != nil {
		return personalsvc.ManagerUnknown, newError("manager state", contextCause(ctx, err))
	}
	return state, nil
}

// Restore reconstructs the independently captured artifact and manager
// snapshots after a failed mutation. It never infers one snapshot from the
// other, so either side may have been absent before the operation.
func (a *Adapter) Restore(
	ctx context.Context,
	previousArtifact personalsvc.Artifact,
	previousManager personalsvc.ManagerRegistration,
) error {
	if err := a.available(ctx); err != nil {
		return err
	}
	artifactPlan, artifactExists, err := a.planForArtifact(previousArtifact)
	if err != nil {
		return err
	}
	managerPlan, managerExists, err := a.planForManager(previousManager)
	if err != nil {
		return err
	}
	currentArtifact, currentManager, err := a.mutableState(ctx)
	if err != nil {
		return err
	}
	if currentArtifact.State() != personalsvc.RegistrationInstalled &&
		currentArtifact.State() != personalsvc.RegistrationNotInstalled {
		return newError("restore", nil)
	}
	if currentManager.Ownership() != personalsvc.ManagerOwnershipOwned &&
		currentManager.Ownership() != personalsvc.ManagerOwnershipNotLoaded {
		return newError("restore", nil)
	}
	if currentManager.Ownership() == personalsvc.ManagerOwnershipOwned {
		state, stateErr := a.scheduler.State(ctx)
		if stateErr != nil || state == personalsvc.ManagerUnknown {
			return newError("restore", contextCause(ctx, stateErr))
		}
		if state == personalsvc.ManagerActive {
			if err := a.scheduler.End(ctx); err != nil {
				return newError("restore", contextCause(ctx, err))
			}
		}
		if err := a.scheduler.Disable(ctx); err != nil {
			return newError("restore", contextCause(ctx, err))
		}
		if err := a.scheduler.Delete(ctx); err != nil {
			return newError("restore", contextCause(ctx, err))
		}
	}
	if currentArtifact.State() == personalsvc.RegistrationInstalled {
		if err := a.artifacts.Remove(ctx, a.artifactPath); err != nil {
			return newError("restore", contextCause(ctx, err))
		}
	}

	if managerExists {
		if err := a.writePlan(ctx, managerPlan); err != nil {
			return err
		}
		if err := a.scheduler.Create(ctx, a.artifactPath); err != nil {
			return newError("restore", contextCause(ctx, err))
		}
		if err := a.scheduler.Enable(ctx); err != nil {
			return newError("restore", contextCause(ctx, err))
		}
	}
	if artifactExists {
		return a.writePlan(ctx, artifactPlan)
	}
	if managerExists {
		if err := a.artifacts.Remove(ctx, a.artifactPath); err != nil {
			return newError("restore", contextCause(ctx, err))
		}
	}
	return nil
}

func (a *Adapter) planForArtifact(artifact personalsvc.Artifact) (personalsvc.TaskSchedulerSpec, bool, error) {
	if artifact.State() == personalsvc.RegistrationNotInstalled {
		return personalsvc.TaskSchedulerSpec{}, false, nil
	}
	if artifact.State() != personalsvc.RegistrationInstalled {
		return personalsvc.TaskSchedulerSpec{}, false, newError("restore", nil)
	}
	registration, found := artifact.Registration()
	if !found {
		return personalsvc.TaskSchedulerSpec{}, false, newError("restore", nil)
	}
	snapshot, found := artifact.RestoreSnapshot()
	plan, valid := snapshot.(personalsvc.TaskSchedulerSpec)
	if !found || !valid || plan.Registration() != registration {
		return personalsvc.TaskSchedulerSpec{}, false, newError("restore", nil)
	}
	return plan, true, nil
}

func (a *Adapter) planForManager(manager personalsvc.ManagerRegistration) (personalsvc.TaskSchedulerSpec, bool, error) {
	if manager.Ownership() == personalsvc.ManagerOwnershipNotLoaded {
		return personalsvc.TaskSchedulerSpec{}, false, nil
	}
	if manager.Ownership() != personalsvc.ManagerOwnershipOwned {
		return personalsvc.TaskSchedulerSpec{}, false, newError("restore", nil)
	}
	registration, found := manager.Registration()
	if !found {
		return personalsvc.TaskSchedulerSpec{}, false, newError("restore", nil)
	}
	snapshot, found := manager.RestoreSnapshot()
	plan, valid := snapshot.(personalsvc.TaskSchedulerSpec)
	if !found || !valid || plan.Registration() != registration {
		return personalsvc.TaskSchedulerSpec{}, false, newError("restore", nil)
	}
	return plan, true, nil
}

func (a *Adapter) writePlan(ctx context.Context, plan personalsvc.TaskSchedulerSpec) error {
	document, err := a.renderPlan(plan)
	if err != nil {
		return err
	}
	if err := a.artifacts.Write(ctx, a.artifactPath, document); err != nil {
		return newError("restore", contextCause(ctx, err))
	}
	return nil
}

func (a *Adapter) mutableState(ctx context.Context) (personalsvc.Artifact, personalsvc.ManagerRegistration, error) {
	artifact, err := a.InspectArtifact(ctx)
	if err != nil {
		return personalsvc.UnknownArtifact(), personalsvc.UnknownManager(), err
	}
	manager, err := a.InspectManager(ctx)
	if err != nil {
		return personalsvc.UnknownArtifact(), personalsvc.UnknownManager(), err
	}
	return artifact, manager, nil
}

func (a *Adapter) render() ([]byte, error) {
	return a.renderPlan(a.plan)
}

func (a *Adapter) renderPlan(plan personalsvc.TaskSchedulerSpec) ([]byte, error) {
	document, err := plan.XML()
	if err != nil {
		return nil, newError("render", nil)
	}
	resolved, ok := resolveTaskDocument(document, a.taskName, userIdentity{
		sid: a.userSID, account: a.userAccount, name: a.userName, lookup: a.userLookup,
	}, plan.StartOnLogin())
	if !ok {
		return nil, newError("render", nil)
	}
	return resolved, nil
}

func resolveTaskDocument(document []byte, taskName string, identity userIdentity, startOnLogin bool) ([]byte, bool) {
	text, ok := decodeTaskDocument(document)
	if !ok {
		return nil, false
	}
	oldURI := taskXMLLeaf("URI", personalsvc.TaskSchedulerTaskName)
	oldUser := taskXMLLeaf("UserId", personalsvc.TaskSchedulerInteractiveUser)
	wantUsers := 1
	if startOnLogin {
		wantUsers = 2
	}
	if strings.Count(text, oldURI) != 1 || strings.Count(text, oldUser) != wantUsers {
		return nil, false
	}
	text = strings.Replace(text, oldURI, taskXMLLeaf("URI", taskName), 1)
	if startOnLogin {
		text = strings.Replace(text, oldUser, taskXMLLeaf("UserId", identity.account), 1)
	}
	text = strings.Replace(text, oldUser, taskXMLLeaf("UserId", identity.sid), 1)
	return encodeTaskDocument(text), true
}

func (a *Adapter) parse(document []byte) (personalsvc.TaskSchedulerSpec, bool) {
	normalized, ok := rewriteTaskDocumentIdentity(
		document,
		a.taskName,
		personalsvc.TaskSchedulerTaskName,
		userIdentity{sid: a.userSID, account: a.userAccount, name: a.userName, lookup: a.userLookup},
		personalsvc.TaskSchedulerInteractiveUser,
	)
	if !ok {
		return personalsvc.TaskSchedulerSpec{}, false
	}
	plan, err := personalsvc.ParseTaskSchedulerXML(normalized)
	if err != nil {
		return personalsvc.TaskSchedulerSpec{}, false
	}
	return plan, true
}

func (a *Adapter) available(ctx context.Context) error {
	if a == nil || a.scheduler == nil || a.artifacts == nil || a.httpClient == nil || ctx == nil {
		return newError("configuration", nil)
	}
	if err := ctx.Err(); err != nil {
		return newError("operation", err)
	}
	return nil
}

func rewriteTaskDocument(document []byte, oldTask, newTask, oldUser, newUser string) ([]byte, bool) {
	return rewriteTaskDocumentUsers(document, oldTask, newTask, []string{oldUser}, newUser)
}

func rewriteTaskDocumentUsers(
	document []byte,
	oldTask string,
	newTask string,
	oldUsers []string,
	newUser string,
) ([]byte, bool) {
	text, ok := decodeTaskDocument(document)
	if !ok {
		return nil, false
	}
	oldURI := taskXMLLeaf("URI", oldTask)
	if strings.Count(text, oldURI) != 1 {
		return nil, false
	}
	totalUsers := strings.Count(text, "<UserId>")
	if totalUsers < 1 || totalUsers > 2 {
		return nil, false
	}
	matchedUsers := 0
	seen := make(map[string]struct{}, len(oldUsers))
	for _, oldUser := range oldUsers {
		if _, duplicate := seen[oldUser]; duplicate {
			continue
		}
		seen[oldUser] = struct{}{}
		oldUserID := taskXMLLeaf("UserId", oldUser)
		matches := strings.Count(text, oldUserID)
		matchedUsers += matches
		text = strings.ReplaceAll(text, oldUserID, taskXMLLeaf("UserId", newUser))
	}
	if matchedUsers != totalUsers {
		return nil, false
	}
	text = strings.Replace(text, oldURI, taskXMLLeaf("URI", newTask), 1)
	return encodeTaskDocument(text), true
}

func rewriteTaskDocumentIdentity(
	document []byte,
	oldTask string,
	newTask string,
	identity userIdentity,
	newUser string,
) ([]byte, bool) {
	text, ok := decodeTaskDocument(document)
	if !ok {
		return nil, false
	}
	oldURI := taskXMLLeaf("URI", oldTask)
	if strings.Count(text, oldURI) != 1 {
		return nil, false
	}
	userIDs, ok := taskDocumentUserIDs(text)
	if !ok || len(userIDs) < 1 || len(userIDs) > 2 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if !identity.matches(userID) {
			return nil, false
		}
		if _, duplicate := seen[userID]; duplicate {
			continue
		}
		seen[userID] = struct{}{}
		leaf := taskXMLLeaf("UserId", userID)
		if strings.Count(text, leaf) == 0 {
			return nil, false
		}
		text = strings.ReplaceAll(text, leaf, taskXMLLeaf("UserId", newUser))
	}
	text = strings.Replace(text, oldURI, taskXMLLeaf("URI", newTask), 1)
	return encodeTaskDocument(text), true
}

func taskDocumentUserIDs(document string) ([]string, bool) {
	decoder := xml.NewDecoder(strings.NewReader(document))
	decoder.Strict = true
	decoder.CharsetReader = func(label string, input io.Reader) (io.Reader, error) {
		if strings.EqualFold(label, "UTF-16") {
			return input, nil
		}
		return nil, errors.New("unsupported task document encoding")
	}
	userIDs := make([]string, 0, 2)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return userIDs, true
		}
		if err != nil {
			return nil, false
		}
		start, found := token.(xml.StartElement)
		if !found || start.Name.Space != taskXMLNamespace || start.Name.Local != "UserId" {
			continue
		}
		var userID string
		if err := decoder.DecodeElement(&userID, &start); err != nil || userID == "" || len(userIDs) == 2 {
			return nil, false
		}
		userIDs = append(userIDs, userID)
	}
}

func taskXMLLeaf(name, value string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(value))
	return "<" + name + ">" + escaped.String() + "</" + name + ">"
}

func decodeTaskDocument(document []byte) (string, bool) {
	if len(document) >= 2 && document[0] == 0xff && document[1] == 0xfe {
		return decodeUTF16(document[2:])
	}
	if len(document) >= 2 && document[0] == '<' && document[1] == 0 {
		return decodeUTF16(document)
	}
	if bytes.HasPrefix(document, []byte{0xef, 0xbb, 0xbf}) {
		document = document[3:]
	}
	if !utf8.Valid(document) {
		return "", false
	}
	return string(document), true
}

func decodeUTF16(document []byte) (string, bool) {
	if len(document)%2 != 0 {
		return "", false
	}
	units := make([]uint16, 0, len(document)/2)
	for index := 0; index < len(document); index += 2 {
		units = append(units, uint16(document[index])|uint16(document[index+1])<<8)
	}
	return string(utf16.Decode(units)), true
}

func encodeTaskDocument(document string) []byte {
	units := utf16.Encode([]rune(document))
	encoded := make([]byte, 2, 2+len(units)*2)
	encoded[0], encoded[1] = 0xff, 0xfe
	for _, unit := range units {
		encoded = append(encoded, byte(unit), byte(unit>>8))
	}
	return encoded
}

func parseTaskState(output []byte) (personalsvc.ManagerState, error) {
	text, ok := decodeTaskDocument(output)
	if !ok {
		return personalsvc.ManagerUnknown, newError("manager state", nil)
	}
	reader := csv.NewReader(strings.NewReader(text))
	reader.FieldsPerRecord = -1
	record, err := reader.Read()
	if err != nil || len(record) < 7 {
		return personalsvc.ManagerUnknown, newError("manager state", nil)
	}
	if _, err := reader.Read(); err != io.EOF {
		return personalsvc.ManagerUnknown, newError("manager state", nil)
	}
	result, ok := parseTaskResult(strings.TrimSpace(record[6]))
	if !ok {
		return personalsvc.ManagerUnknown, newError("manager state", nil)
	}
	switch result {
	case 0x41301, 0x41325:
		return personalsvc.ManagerActive, nil
	case 0:
		return personalsvc.ManagerInactive, nil
	default:
		if result >= 0x41300 && result < 0x41400 {
			return personalsvc.ManagerInactive, nil
		}
		return personalsvc.ManagerFailed, nil
	}
}

func parseTaskResult(value string) (uint32, bool) {
	if value == "" {
		return 0, false
	}
	if strings.HasPrefix(value, "-") {
		parsed, err := strconv.ParseInt(value, 10, 32)
		return uint32(parsed), err == nil
	}
	parsed, err := strconv.ParseUint(value, 0, 32)
	return uint32(parsed), err == nil
}

func validUserDataRoot(root string) bool {
	if root == "" || filepath.Clean(root) != root || !filepath.IsAbs(root) {
		return false
	}
	volume := filepath.VolumeName(root)
	if strings.EqualFold(root, volume+string(filepath.Separator)) {
		return false
	}
	lower := strings.ToLower(filepath.Clean(root))
	for _, global := range []string{
		strings.ToLower(filepath.Join(volume+string(filepath.Separator), "ProgramData")),
		strings.ToLower(filepath.Join(volume+string(filepath.Separator), "Windows")),
	} {
		if lower == global || strings.HasPrefix(lower, global+string(filepath.Separator)) {
			return false
		}
	}
	return true
}

func validTaskName(name string) bool {
	return name == personalsvc.TaskSchedulerTaskName ||
		(strings.HasPrefix(name, testTaskNamePrefix) && len(name) > len(testTaskNamePrefix) && len(name) <= 238)
}

func validInteractiveSID(sid string) bool {
	if !strings.HasPrefix(sid, "S-1-") || strings.TrimSpace(sid) != sid {
		return false
	}
	switch strings.ToUpper(sid) {
	case "S-1-5-18", "S-1-5-19", "S-1-5-20":
		return false
	default:
		return true
	}
}

type userIdentity struct {
	sid     string
	account string
	name    string
	lookup  func(string) bool
}

func (i userIdentity) matches(value string) bool {
	if value == i.sid || strings.EqualFold(value, i.account) || strings.EqualFold(value, i.name) {
		return true
	}
	return i.lookup != nil && i.lookup(value)
}

func validUserIdentity(identity userIdentity) bool {
	if !validInteractiveSID(identity.sid) || identity.account == "" || identity.name == "" ||
		strings.TrimSpace(identity.account) != identity.account || strings.TrimSpace(identity.name) != identity.name ||
		strings.ContainsAny(identity.account, "\r\n\x00") || strings.ContainsAny(identity.name, "\r\n\x00") {
		return false
	}
	switch strings.ToLower(identity.account) {
	case "system", `nt authority\system`, "local service", `nt authority\local service`,
		"network service", `nt authority\network service`:
		return false
	default:
		return true
	}
}

func contextCause(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func newError(operation string, cause error) *Error {
	if !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		cause = nil
	}
	return &Error{operation: operation, cause: cause}
}
