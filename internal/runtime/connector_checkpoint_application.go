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

package runtime

import (
	"context"
	"encoding/json/jsontext"
	"errors"

	"github.com/ob-labs/powercontext-go/source"
)

// ConnectorCheckpointConflictError reports a stale checkpoint or a binding
// identity mismatch without exposing the binding or stored value.
type ConnectorCheckpointConflictError struct{}

func (*ConnectorCheckpointConflictError) Error() string {
	return "Connector checkpoint conflicts with the stored binding or value"
}

// ConnectorCheckpointApplication admits checkpoint access independently of
// Connector execution. Its store owns each short read or CAS transaction.
type ConnectorCheckpointApplication struct {
	runtime *Runtime
	store   ConnectorCheckpointStore
}

func NewConnectorCheckpointApplication(runtime *Runtime, store ConnectorCheckpointStore) (*ConnectorCheckpointApplication, error) {
	if runtime == nil || store == nil {
		return nil, errors.New("runtime: Connector checkpoint dependencies must not be nil")
	}
	return &ConnectorCheckpointApplication{runtime: runtime, store: store}, nil
}

// Get preserves the actual row presence; transports represent both absence
// and a stored JSON null as the wire checkpoint null.
func (a *ConnectorCheckpointApplication) Get(ctx context.Context, binding source.ConnectorBinding) (source.ConnectorCheckpoint, error) {
	if cancelErr := ctx.Err(); cancelErr != nil {
		return source.NoConnectorCheckpoint(), cancelErr
	}
	if bindingErr := binding.Validate(); bindingErr != nil {
		return source.NoConnectorCheckpoint(), bindingErr
	}
	var result source.ConnectorCheckpoint
	err := a.runtime.ScopedRead(ctx, binding.ScopeID(), func(ctx context.Context, _ string) error {
		if cancelErr := ctx.Err(); cancelErr != nil {
			return cancelErr
		}
		value, loadErr := a.store.Load(ctx, binding)
		result = value
		return loadErr
	})
	if cancelErr := ctx.Err(); cancelErr != nil {
		return source.NoConnectorCheckpoint(), cancelErr
	}
	if err != nil {
		return source.NoConnectorCheckpoint(), connectorCheckpointError(err)
	}
	return result, nil
}

// Commit compares the expected wire value semantically, treating absence as
// JSON null only for that comparison. The actual row presence and value remain
// the final store CAS expectation, including an initial null-to-null commit.
func (a *ConnectorCheckpointApplication) Commit(
	ctx context.Context,
	binding source.ConnectorBinding,
	checkpoint, expected jsontext.Value,
) (source.ConnectorCheckpoint, error) {
	if cancelErr := ctx.Err(); cancelErr != nil {
		return source.NoConnectorCheckpoint(), cancelErr
	}
	if bindingErr := binding.Validate(); bindingErr != nil {
		return source.NoConnectorCheckpoint(), bindingErr
	}
	var result source.ConnectorCheckpoint
	err := a.runtime.ScopedWrite(ctx, binding.ScopeID(), func(ctx context.Context, _ string) error {
		next, nextErr := source.NewConnectorCheckpoint(checkpoint)
		if nextErr != nil {
			return nextErr
		}
		comparison, expectedErr := source.NewConnectorCheckpoint(expected)
		if expectedErr != nil {
			return expectedErr
		}
		if cancelErr := ctx.Err(); cancelErr != nil {
			return cancelErr
		}
		actual, loadErr := a.store.Load(ctx, binding)
		if cancelErr := ctx.Err(); cancelErr != nil {
			return cancelErr
		}
		if loadErr != nil {
			return loadErr
		}
		comparable := actual
		if !actual.Found() {
			var nullErr error
			comparable, nullErr = source.NewConnectorCheckpoint(jsontext.Value("null"))
			if nullErr != nil {
				return nullErr
			}
		}
		equal, compareErr := source.EqualConnectorCheckpoints(comparable, comparison)
		if compareErr != nil {
			return compareErr
		}
		if !equal {
			return &ConnectorCheckpointConflictError{}
		}
		if cancelErr := ctx.Err(); cancelErr != nil {
			return cancelErr
		}
		if saveErr := a.store.Save(ctx, binding, next, actual); saveErr != nil {
			return saveErr
		}
		result = next
		return nil
	})
	if cancelErr := ctx.Err(); cancelErr != nil {
		return source.NoConnectorCheckpoint(), cancelErr
	}
	if err != nil {
		return source.NoConnectorCheckpoint(), connectorCheckpointError(err)
	}
	return result, nil
}

func connectorCheckpointError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// Storage adapters expose only this classification, without a dependency
	// on Runtime or a string match against a potentially sensitive diagnostic.
	if _, ok := errors.AsType[interface {
		error
		ConnectorCheckpointConflict()
	}](err); ok {
		return &ConnectorCheckpointConflictError{}
	}
	return err
}
