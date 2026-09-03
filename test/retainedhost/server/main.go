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

// Command retainedhost-server exposes the real Go Server stack to retained
// adapter tests while replacing only non-deterministic model extraction.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/server"
	"github.com/ob-labs/powercontext-go/source"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	host := flag.String("host", "127.0.0.1", "loopback host")
	port := flag.Int("port", 0, "loopback port")
	database := flag.String("database", "", "absolute SQLite database path")
	sourceWindowLimit := flag.Int64("source-window-limit", 100, "Sources processed by one flush")
	flag.Parse()
	if *database == "" || !filepath.IsAbs(*database) || *port < 1 || *sourceWindowLimit < 1 {
		return errors.New("retained host test server requires an absolute database path, port, and positive source window limit")
	}
	config, err := server.DefaultConfig()
	if err != nil {
		return err
	}
	config.HTTP = server.HTTPConfig{Host: *host, Port: *port}
	config.MCP.Enabled = false
	config.Dashboard.Enabled = false
	config.HandoffReport.Enabled = false
	config.Metrics.Enabled = false
	config.Logging.Access = false
	config.Runtime.SourceWindowLimit = *sourceWindowLimit
	config.Database.SQLite.URL = "sqlite+aiosqlite:///" + filepath.ToSlash(*database)

	application, err := server.OpenApplication(context.Background(), config, server.Dependencies{MemoryCandidates: capturedSourcePipeline{}})
	if err != nil {
		return err
	}
	defer func() { _ = application.Close(context.Background()) }()
	handler, err := application.HTTPHandler()
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: config.HTTP.Address(), Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	return httpServer.ListenAndServe()
}

type capturedSourcePipeline struct{}

func (capturedSourcePipeline) Extract(_ context.Context, request memory.CandidateRequest) ([]memory.EntryInput, error) {
	entries := make([]memory.EntryInput, 0, len(request.Sources()))
	for _, value := range request.Sources() {
		content, ok := value.(source.ContentSource)
		if !ok {
			continue
		}
		entries = append(entries, memory.NewEntryInput(nil, "fact", content.Content(), []source.Value{value}, nil, nil))
	}
	return entries, nil
}
