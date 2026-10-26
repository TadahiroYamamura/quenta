// Package db は、データベースの構造の定義（schema.sql）と移行（migrations/）を Go から読めるようにする。
// 構造は人がレビューするので、Go のコードに文字列として書かず、SQL のファイルに置く（CLAUDE.md の「構成」）
package db

import "embed"

// Schema は、今の構造の全体。データベースには当てず、移行を全部当てた構造と比べるテストで使う
//
//go:embed schema.sql
var Schema string

//go:embed migrations/*.sql
var Migrations embed.FS
