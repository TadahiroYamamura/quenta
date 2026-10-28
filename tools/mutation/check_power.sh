#!/usr/bin/env bash
# 実行時の確認の検出力を測る。操作の実装を 1 か所ずつ壊し、確認のほかに判定を持たないテスト（internal/ops/checkpower_test.go）で動かして、
# 実行時の確認（ops.ErrCheckFailed）が捕まえた変異の数を数える。壊したファイルは、1 つ測るごとに元に戻す
set -u
cd "$(dirname "$0")/../.."
mutants=(
  'internal/task/task.go|Task:  Task{ID: id, Title: title, Body: body, Status: Todo, CreatedAt: now},|Task:  Task{ID: id, Title: title, Body: body, Status: Done, CreatedAt: now},|登録のタスクの状態を done にする'
  'internal/task/task.go|Event: Event{TaskID: id, Actor: actor, From: nil, To: Todo, Reason: "", At: now},|Event: Event{TaskID: id, Actor: actor, From: nil, To: Done, Reason: "", At: now},|登録のイベントの変更後を done にする'
  'internal/task/task.go|Event: Event{TaskID: id, Actor: actor, From: nil, To: Todo, Reason: "", At: now},|Event: Event{TaskID: id + "x", Actor: actor, From: nil, To: Todo, Reason: "", At: now},|登録のイベントのタスクの ID を変える'
  'internal/task/task.go|Event: Event{TaskID: id, Actor: actor, From: nil, To: Todo, Reason: "", At: now},|Event: Event{TaskID: id, Actor: "", From: nil, To: Todo, Reason: "", At: now},|登録のイベントの主体を空にする'
  'internal/task/task.go|Event: Event{TaskID: id, Actor: actor, From: nil, To: Todo, Reason: "", At: now},|Event: Event{TaskID: id, Actor: actor, From: nil, To: Todo, Reason: title, At: now},|登録のイベントの理由にタイトルを入れる'
  'internal/task/task.go|Task:  Task{ID: id, Title: title, Body: body, Status: Todo, CreatedAt: now},|Task:  Task{ID: id, Title: body, Body: title, Status: Todo, CreatedAt: now},|登録のタイトルと本文を入れ替える'
  'internal/task/task.go|		Status: to,|		Status: found.Status,|完了・中止で状態を変えない'
  'internal/task/task.go|Event:  Event{TaskID: id, Actor: actor, From: \&from, To: to, Reason: reason, At: now},|Event:  Event{TaskID: id, Actor: actor, From: \&from, To: found.Status, Reason: reason, At: now},|完了・中止のイベントの変更後をもとの状態にする'
  'internal/task/task.go|Event:  Event{TaskID: id, Actor: actor, From: \&from, To: to, Reason: reason, At: now},|Event:  Event{TaskID: id + "x", Actor: actor, From: \&from, To: to, Reason: reason, At: now},|完了・中止のイベントのタスクの ID を変える'
  'internal/task/task.go|Event:  Event{TaskID: id, Actor: actor, From: \&from, To: to, Reason: reason, At: now},|Event:  Event{TaskID: id, Actor: actor, From: \&to, To: to, Reason: reason, At: now},|完了・中止のイベントの変更前を行き先にする'
  'internal/task/task.go|Event:  Event{TaskID: id, Actor: actor, From: \&from, To: to, Reason: reason, At: now},|Event:  Event{TaskID: id, Actor: actor, From: \&from, To: to, Reason: "", At: now},|完了・中止のイベントの理由を空にする'
  'internal/task/task.go|	case Canceling:\n		return Canceled, true|	case Canceling:\n		return Done, true|中止の行き先を done にする'
)
total=0; caught=0
for m in "${mutants[@]}"; do
  IFS='|' read -r file from to label <<<"$m"
  cp "$file" "$file.orig"
  python3 -I -c 'import sys;p,f,t=sys.argv[1:];s=open(p).read();f=f.replace("\\n","\n");t=t.replace("\\n","\n");f=f.replace("\\&","&");t=t.replace("\\&","&");assert s.count(f)==1,f;open(p,"w").write(s.replace(f,t))' "$file" "$from" "$to" || { mv "$file.orig" "$file"; echo "当てられない変異: $label" >&2; exit 1; }
  out=$(go test -count=1 -tags checkpower -run 'Test操作を実行してエラーにならない' ./internal/ops/ 2>&1)
  mv "$file.orig" "$file"
  total=$((total+1))
  if grep -q '実行時の確認が' <<<"$out"; then caught=$((caught+1)); r="捕まえた"; elif grep -q '^ok' <<<"$out"; then r="捕まえない"; else r="ほかで失敗"; fi
  echo "$r: $label"
done
echo "実行時の確認の検出力: $caught / $total"
