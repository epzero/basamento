-- +goose Up
CREATE TABLE authz_bindings (
    tenant    TEXT NOT NULL,
    project   TEXT NOT NULL,
    principal TEXT NOT NULL,
    role      TEXT NOT NULL,
    condition TEXT,
    PRIMARY KEY (tenant, project, principal, role)
);

-- +goose Down
DROP TABLE authz_bindings;