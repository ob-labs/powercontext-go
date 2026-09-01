// Copyright (c) 2026 OceanBase.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package webui

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/review"
)

const pageCSP = "default-src 'none'; style-src 'self'; script-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'"

//go:embed templates/*.html templates/*.tmpl static/*
var assets embed.FS

// Scope is one explicit Server scope exposed by the personal Dashboard.
// It is deliberately configuration-owned; the Dashboard never enumerates
// scope identifiers from persistence.
type Scope struct {
	ScopeID     string `json:"scope_id"`
	DisplayName string `json:"display_name"`
}

type Options struct {
	DashboardEnabled       bool
	Scopes                 []Scope
	HandoffReportEnabled   bool
	AuthenticationRequired bool
	AgentSkillTargets      []skill.AgentSkillTarget
	SkillProjections       SkillProjectionOperations
	Logger                 *slog.Logger
}

type SkillProjectionOperations interface {
	GetCandidate(context.Context, string, string) (review.Snapshot, error)
	GetSkill(context.Context, string, artifact.Ref) (skill.Skill, error)
	ListExternalSkills(context.Context, string, bool) ([]skill.Resolution, error)
	ScanExternalSkills(context.Context, string) (skill.ProviderScan, error)
}

type pages struct {
	dashboard         *template.Template
	skills            *template.Template
	review            *template.Template
	handoff           *template.Template
	scopes            []Scope
	options           Options
	static            http.Handler
	projectionTargets []skill.AgentSkillTarget
	projections       SkillProjectionOperations
	logger            *slog.Logger
}

type pageData struct {
	Title                  string
	ActivePage             string
	StatusTitleKey         string
	StatusTitle            string
	HandoffReportEnabled   bool
	DashboardEnabled       bool
	SkillsEnabled          bool
	ReviewEnabled          bool
	AuthenticationRequired bool
	HomeRoute              string
}

// Mount registers only the frozen Dashboard surface on mux. The caller keeps
// ownership of authentication, request IDs, API fallback, and listener state.
func Mount(mux *http.ServeMux, options Options) error {
	if mux == nil {
		return errors.New("webui: mux must not be nil")
	}
	if !options.DashboardEnabled && !options.HandoffReportEnabled {
		return errors.New("webui: at least one Web UI feature must be enabled")
	}
	var dashboard *template.Template
	var err error
	if options.DashboardEnabled {
		dashboard, err = parsePage("dashboard.html")
		if err != nil {
			return err
		}
	}
	var skillsPage, reviewPage *template.Template
	if options.DashboardEnabled {
		skillsPage, err = parsePage("skills.html")
		if err != nil {
			return err
		}
		reviewPage, err = parsePage("review.html")
		if err != nil {
			return err
		}
	}
	var handoff *template.Template
	if options.HandoffReportEnabled {
		handoff, err = parsePage("handoff_report.html")
		if err != nil {
			return err
		}
	}
	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		return err
	}
	owner := &pages{
		dashboard: dashboard,
		skills:    skillsPage,
		review:    reviewPage,
		handoff:   handoff,
		scopes:    append([]Scope{}, options.Scopes...),
		options:   options,
		static:    http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))),
		logger:    options.Logger,
	}
	owner.projections = options.SkillProjections
	for _, target := range options.AgentSkillTargets {
		if target.AllowManagedPublish() {
			owner.projectionTargets = append(owner.projectionTargets, target)
		}
	}
	if dashboard != nil {
		mux.HandleFunc("GET /{$}", owner.dashboardPage)
		mux.HandleFunc("GET /dashboard/scopes", owner.dashboardScopes)
		mux.HandleFunc("GET /skills", owner.skillsPage)
		mux.HandleFunc("GET /reviews", owner.reviewPage)
		mux.HandleFunc("POST /dashboard/skill-projections/status", owner.skillProjectionStatus)
		mux.HandleFunc("POST /dashboard/skill-projections/publish", owner.skillProjectionPublish)
	}
	mux.Handle("GET /static/", owner.static)
	if handoff != nil {
		mux.HandleFunc("GET /handoff-reports", owner.handoffPage)
	}
	return nil
}

func parsePage(name string) (*template.Template, error) {
	return template.New(name).ParseFS(
		assets,
		"templates/components.tmpl",
		"templates/activity_heatmap.html",
		"templates/recall_trend.html",
		"templates/"+name,
	)
}

func (p *pages) dashboardPage(writer http.ResponseWriter, _ *http.Request) {
	p.writePage(writer, p.dashboard, pageData{
		Title: "PowerContext Dashboard", ActivePage: "dashboard",
		StatusTitleKey: "dashboardTitle", StatusTitle: "Dashboard",
		HandoffReportEnabled: p.options.HandoffReportEnabled, DashboardEnabled: true,
		SkillsEnabled: true, ReviewEnabled: true,
		AuthenticationRequired: p.options.AuthenticationRequired, HomeRoute: "/",
	})
}

func (p *pages) skillsPage(writer http.ResponseWriter, _ *http.Request) {
	p.writePage(writer, p.skills, pageData{
		Title: "PowerContext Skills Library", ActivePage: "skills",
		StatusTitleKey: "skillsTitle", StatusTitle: "Skills",
		HandoffReportEnabled: p.options.HandoffReportEnabled, DashboardEnabled: true,
		SkillsEnabled: true, ReviewEnabled: true,
		AuthenticationRequired: p.options.AuthenticationRequired, HomeRoute: "/",
	})
}

func (p *pages) reviewPage(writer http.ResponseWriter, _ *http.Request) {
	p.writePage(writer, p.review, pageData{
		Title: "PowerContext Review", ActivePage: "review",
		StatusTitleKey: "reviewTitle", StatusTitle: "Review",
		HandoffReportEnabled: p.options.HandoffReportEnabled, DashboardEnabled: true,
		SkillsEnabled: true, ReviewEnabled: true,
		AuthenticationRequired: p.options.AuthenticationRequired, HomeRoute: "/",
	})
}

func (p *pages) handoffPage(writer http.ResponseWriter, _ *http.Request) {
	homeRoute := "/handoff-reports"
	if p.options.DashboardEnabled {
		homeRoute = "/"
	}
	p.writePage(writer, p.handoff, pageData{
		Title: "PowerContext Handoff Report", ActivePage: "handoff_report",
		StatusTitleKey: "handoffReportTitle", StatusTitle: "Handoff Report",
		HandoffReportEnabled: true, DashboardEnabled: p.options.DashboardEnabled,
		SkillsEnabled: p.options.DashboardEnabled, ReviewEnabled: p.options.DashboardEnabled,
		AuthenticationRequired: p.options.AuthenticationRequired,
		HomeRoute:              homeRoute,
	})
}

func (*pages) writePage(writer http.ResponseWriter, page *template.Template, data pageData) {
	var rendered bytes.Buffer
	if page == nil || page.ExecuteTemplate(&rendered, "page", data) != nil {
		http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Security-Policy", pageCSP)
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Content-Length", strconv.Itoa(rendered.Len()))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(rendered.Bytes())
}

func (p *pages) dashboardScopes(writer http.ResponseWriter, _ *http.Request) {
	payload, err := json.Marshal(p.scopes)
	if err != nil {
		http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(payload)
}
