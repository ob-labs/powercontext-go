/*
 * Copyright (c) 2026 OceanBase.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

//go:build cgo && (darwin || linux)

#include "loader.h"

#include <dlfcn.h>
#include <stdlib.h>
#include <string.h>

typedef int (*SeekDBOpen)(const char *, const char **, void **);
typedef int (*SeekDBClose)(void *);
typedef int (*SeekDBOptions)(void *, PCSeekDBConnectionOptions *);

struct PCSeekDBLibrary {
    void *dynamic_library;
    SeekDBOpen open;
    SeekDBClose close;
    SeekDBOptions options;
};

static int pc_seekdb_symbol(
    void *dynamic_library,
    const char *name,
    void **out_symbol,
    char **out_error
) {
    dlerror();
    *out_symbol = dlsym(dynamic_library, name);
    const char *failure = dlerror();
    if (failure == NULL && *out_symbol != NULL) {
        return 0;
    }
    if (out_error != NULL) {
        *out_error = strdup(failure == NULL ? "seekDB symbol is unavailable" : failure);
    }
    return -1;
}

int pc_seekdb_library_open(const char *path, PCSeekDBLibrary **out_library, char **out_error) {
    if (path == NULL || *path == '\0' || out_library == NULL) {
        return -1;
    }
    *out_library = NULL;
    if (out_error != NULL) {
        *out_error = NULL;
    }
    void *dynamic_library = dlopen(path, RTLD_NOW | RTLD_LOCAL);
    if (dynamic_library == NULL) {
        if (out_error != NULL) {
            const char *failure = dlerror();
            *out_error = strdup(failure == NULL ? "seekDB library could not be loaded" : failure);
        }
        return -1;
    }
    PCSeekDBLibrary *library = calloc(1, sizeof(*library));
    if (library == NULL) {
        dlclose(dynamic_library);
        return -1;
    }
    library->dynamic_library = dynamic_library;
    if (pc_seekdb_symbol(dynamic_library, "seekdb_open", (void **)&library->open, out_error) != 0 ||
        pc_seekdb_symbol(dynamic_library, "seekdb_close", (void **)&library->close, out_error) != 0 ||
        pc_seekdb_symbol(
            dynamic_library,
            "seekdb_connection_options",
            (void **)&library->options,
            out_error
        ) != 0) {
        dlclose(dynamic_library);
        free(library);
        return -1;
    }
    *out_library = library;
    return 0;
}

void pc_seekdb_library_close(PCSeekDBLibrary *library) {
    if (library == NULL) {
        return;
    }
    if (library->dynamic_library != NULL) {
        dlclose(library->dynamic_library);
    }
    free(library);
}

int pc_seekdb_instance_open(
    PCSeekDBLibrary *library,
    const char *directory,
    void **out_handle
) {
    if (library == NULL || library->open == NULL || directory == NULL || out_handle == NULL) {
        return -2;
    }
    return library->open(directory, NULL, out_handle);
}

int pc_seekdb_connection_options(
    PCSeekDBLibrary *library,
    void *handle,
    PCSeekDBConnectionOptions *out_options
) {
    if (library == NULL || library->options == NULL || handle == NULL || out_options == NULL) {
        return -2;
    }
    memset(out_options, 0, sizeof(*out_options));
    return library->options(handle, out_options);
}

int pc_seekdb_instance_close(PCSeekDBLibrary *library, void *handle) {
    if (library == NULL || library->close == NULL || handle == NULL) {
        return -2;
    }
    return library->close(handle);
}

void pc_seekdb_error_free(char *message) {
    free(message);
}
