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

package server

import (
	"context"
	"fmt"

	pcruntime "github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

type applicationStorage struct {
	database          *sqlstore.Database
	resource          pcruntime.Resource
	dialect           sqlstore.Dialect
	artifactCursorKey [32]byte
}

func openApplicationStorage(ctx context.Context, config DatabaseConfig) (applicationStorage, error) {
	storage := applicationStorage{dialect: sqlstore.SQLiteDialect}
	var err error
	switch config.Kind {
	case "sqlite":
		var dsn string
		dsn, err = SQLiteDSN(config.SQLite.URL)
		if err == nil {
			storage.database, err = sqlstore.OpenSQLite(ctx, sqlstore.SQLiteConfig{
				DSN: dsn, BusyTimeout: config.SQLite.BusyTimeout,
				JournalMode: config.SQLite.JournalMode, ForeignKeys: config.SQLite.ForeignKeys,
				MaxOpenConns: config.SQLite.MaxOpenConns, MaxIdleConns: config.SQLite.MaxIdleConns,
				ConnMaxLifetime: config.SQLite.ConnMaxLifetime,
			})
		}
		if err == nil {
			storage.artifactCursorKey, err = loadArtifactCursorKey(ctx, dsn)
			if err != nil {
				_ = storage.database.Close(context.WithoutCancel(ctx))
			}
		}
		storage.resource = storage.database
	case "oceanbase":
		storage.dialect = sqlstore.MySQLDialect
		storage.database, err = sqlstore.OpenOceanBase(ctx, sqlstore.OceanBaseConfig{
			URL:             config.OceanBase.URL,
			MaxOpenConns:    config.OceanBase.MaxOpenConns,
			MaxIdleConns:    config.OceanBase.MaxIdleConns,
			ConnMaxLifetime: config.OceanBase.MaxLifetime,
		})
		storage.resource = storage.database
	case "seekdb":
		storage.dialect = sqlstore.MySQLDialect
		var instance seekDBInstance
		storage.database, instance.value, err = sqlstore.OpenSeekDB(ctx, sqlstore.SeekDBConfig{
			Path: config.SeekDB.Path, Database: config.SeekDB.Database,
			LibraryPath:     config.SeekDB.LibraryPath,
			MaxOpenConns:    config.SeekDB.MaxOpenConns,
			MaxIdleConns:    config.SeekDB.MaxIdleConns,
			ConnMaxLifetime: config.SeekDB.MaxLifetime,
		})
		if err == nil {
			instance.database = storage.database
			storage.resource = &instance
		}
	}
	if err != nil {
		return applicationStorage{}, fmt.Errorf("server: open database: %w", err)
	}
	return storage, nil
}
