// Package db は、DB の構造の定義（schema.sql）と移行（migrations/*.sql）を Go から読めるようにする
package db

import "embed"

//go:embed schema.sql
var Schema string

//go:embed migrations/*.sql
var Migrations embed.FS
