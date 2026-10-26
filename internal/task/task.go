// Package task は、タスクとイベントの Go の型と、Dafny の仕様から変換したコードとの橋渡しを持つ。
package task

import (
	"errors"
	"fmt"
)

type Status string

const (
	StatusTodo     Status = "todo"
	StatusDone     Status = "done"
	StatusCanceled Status = "canceled"
)

type Outcome int

const (
	OutcomeComplete Outcome = iota + 1
	OutcomeCancel
)

type Task struct {
	ID        string
	Title     string
	Body      string
	Status    Status
	CreatedAt int64 // UNIX 時間のミリ秒
}

// From が nil なら登録のイベント
type Event struct {
	TaskID string
	Actor  string
	From   *Status
	To     Status
	Reason *string
	At     int64 // UNIX 時間のミリ秒
}

type Code string

// 仕様のエラーのコードは、仕様の ErrorCode が決める。ここの値が ErrorCode と一致することは、テストで確かめる
const (
	CodeSessionRequired Code = "session_required"
	CodeTitleRequired   Code = "title_required"
	CodeReasonRequired  Code = "reason_required"
	CodeNotFound        Code = "not_found"
	CodeAlreadyFinished Code = "already_finished"
	// 利用者には返さない。登録の処理が ID を作り直す合図
	CodeIDConflict Code = "id_conflict"
	// 以下は仕様の外のエラー
	CodeInvalidArgument Code = "invalid_argument"
	CodeInternal        Code = "internal"
	// データベースが、この版の知らない移行まで当たっている（新しい版が作った）
	CodeUnsupportedDBVersion Code = "unsupported_db_version"
)

// Error は、利用者にエラーコードとして返すエラー
type Error struct {
	Code    Code
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf は、err のエラーコードを返す。エラーがなければ空、Error でないエラーは internal
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code
	}
	return CodeInternal
}

func ParseStatus(s string) (Status, error) {
	switch Status(s) {
	case StatusTodo, StatusDone, StatusCanceled:
		return Status(s), nil
	}
	return "", fmt.Errorf("未知の状態: %q", s)
}
