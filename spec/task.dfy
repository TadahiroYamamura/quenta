// タスクの状態と操作（#1）
//
// 実装（Go）は、ここの関数を変換したものを使う。
// - 1 件のタスクについての判定と遷移: NewTask、FinishArgsError、Finish、IsVisible
// - 全体の状態（State）についての関数: Add、FinishTask、Show、List
//   実装はデータベースで同じことをする。State の関数はその手本で、テストで実装と突き合わせる
//
// ID と現在時刻は仕様の外（Go）で作り、引数で受け取る。
module Quenta {

  datatype Option<T> = None | Some(value: T)

  // ---------- 値 ----------

  datatype Status = Todo | Done | Canceled

  // 状態ごとの判定は、状態をすべて列挙して書く（`_` や `!= Todo` で残りをまとめない）。
  // 状態を足したときに、判定を決めていない箇所が dafny verify でエラーになり、扱いを決め忘れないようにするため
  predicate IsFinished(s: Status)
  {
    match s
    case Todo => false
    case Done | Canceled => true
  }

  // 終わらせ方。Todo を指定できないように、Status とは別の型にする
  datatype Outcome = Complete | Cancel

  function OutcomeStatus(o: Outcome): Status
  {
    match o
    case Complete => Done
    case Cancel => Canceled
  }

  // 時刻（UNIX 時間のミリ秒）。範囲を 64 ビットに収め、Go では int64 に変換されるようにする
  newtype UnixMillis = x: int | -0x8000_0000_0000_0000 <= x < 0x8000_0000_0000_0000

  datatype Task = Task(id: string, title: string, body: string, status: Status, createdAt: UnixMillis)

  // from が None なら登録のイベント。登録のイベントには理由がない
  datatype Event = Event(taskId: string, actor: string, from: Option<Status>, to: Status, reason: Option<string>, at: UnixMillis)

  datatype Error =
    | SessionRequired   // 主体（QUENTA_SESSION）がない
    | TitleRequired     // タイトルが空
    | ReasonRequired    // 理由が空
    | NotFound          // その ID のタスクがない
    | AlreadyFinished   // 終わったタスクの状態を変えようとした
    | IdConflict        // 作った ID が既にある（実装は ID を作り直す）

  datatype Result<T> = Ok(value: T) | Err(error: Error)

  // 空白とみなす文字。Unicode の文字の性質 White_Space で、Go の unicode.IsSpace と同じ（Go 1.26 の src/unicode/graphic.go と tables.go で照らし合わせた）。
  // 全角の空白（U+3000）を含み、ゼロ幅スペース（U+200B）は含まない。Go と一致することは、全文字を回すテストで確かめる
  predicate IsSpace(c: char)
  {
    c == ' ' || c == '\t' || c == '\n' || c == '\U{000B}' || c == '\U{000C}' || c == '\r'
    || c == '\U{0085}' || c == '\U{00A0}' || c == '\U{1680}'
    || ('\U{2000}' <= c <= '\U{200A}')
    || c == '\U{2028}' || c == '\U{2029}' || c == '\U{202F}' || c == '\U{205F}' || c == '\U{3000}'
  }

  // 空、または空白だけ
  predicate IsBlank(s: string)
  {
    forall i :: 0 <= i < |s| ==> IsSpace(s[i])
  }

  // ---------- 1 件のタスクについての判定と遷移 ----------

  // 登録。タイトルなどは入力のまま保存する（空白を削らない）
  function NewTask(id: string, title: string, body: string, actor: string, now: UnixMillis): Result<(Task, Event)>
  {
    if IsBlank(actor) then Err(SessionRequired)
    else if IsBlank(title) then Err(TitleRequired)
    else Ok((Task(id, title, body, Todo, now), Event(id, actor, None, Todo, None, now)))
  }

  // 完了・中止の引数の判定。タスクを探す前に行う
  // （引数が誤っていれば、ID の有無にかかわらず引数のエラーを返す）
  function FinishArgsError(reason: string, actor: string): Option<Error>
  {
    if IsBlank(actor) then Some(SessionRequired)
    else if IsBlank(reason) then Some(ReasonRequired)
    else None
  }

  // 完了・中止
  function Finish(t: Task, o: Outcome, reason: string, actor: string, now: UnixMillis): Result<(Task, Event)>
  {
    match FinishArgsError(reason, actor)
    case Some(e) => Err(e)
    case None =>
      if IsFinished(t.status) then Err(AlreadyFinished)
      else
        var to := OutcomeStatus(o);
        Ok((t.(status := to), Event(t.id, actor, Some(t.status), to, Some(reason), now)))
  }

  // 一覧に出すか。既定は終わっていないタスクだけ
  predicate IsVisible(t: Task, includeFinished: bool)
  {
    includeFinished || !IsFinished(t.status)
  }

  // ---------- 全体の状態 ----------

  // tasks は登録した順
  datatype State = State(tasks: seq<Task>, events: seq<Event>)

  const Empty := State([], [])

  function FindIndex(tasks: seq<Task>, id: string): (r: Option<nat>)
    ensures r.Some? ==> r.value < |tasks| && tasks[r.value].id == id
    ensures r.None? ==> forall i :: 0 <= i < |tasks| ==> tasks[i].id != id
  {
    if |tasks| == 0 then None
    else if tasks[|tasks| - 1].id == id then Some(|tasks| - 1)
    else FindIndex(tasks[..|tasks| - 1], id)
  }

  function Add(st: State, id: string, title: string, body: string, actor: string, now: UnixMillis): Result<State>
  {
    match NewTask(id, title, body, actor, now)
    case Err(e) => Err(e)
    case Ok((t, e)) =>
      if FindIndex(st.tasks, id).Some? then Err(IdConflict)
      else Ok(State(st.tasks + [t], st.events + [e]))
  }

  function FinishTask(st: State, id: string, o: Outcome, reason: string, actor: string, now: UnixMillis): Result<State>
  {
    match FinishArgsError(reason, actor)
    case Some(e) => Err(e)
    case None =>
      match FindIndex(st.tasks, id)
      case None => Err(NotFound)
      case Some(i) =>
        match Finish(st.tasks[i], o, reason, actor, now)
        case Err(e) => Err(e)
        case Ok((t, e)) => Ok(State(st.tasks[i := t], st.events + [e]))
  }

  // そのタスクのイベント（記録した順）
  function EventsOf(events: seq<Event>, id: string): seq<Event>
  {
    if |events| == 0 then []
    else EventsOf(events[..|events| - 1], id)
         + (if events[|events| - 1].taskId == id then [events[|events| - 1]] else [])
  }

  function Show(st: State, id: string): Result<(Task, seq<Event>)>
  {
    match FindIndex(st.tasks, id)
    case None => Err(NotFound)
    case Some(i) => Ok((st.tasks[i], EventsOf(st.events, id)))
  }

  // 一覧: 一覧に出すタスクを、作成日時の古い順に並べる。作成日時が同じなら登録した順
  function List(st: State, includeFinished: bool): seq<Task>
  {
    var picked := Pick(st.tasks, includeFinished, 0);
    PickPositions(st.tasks, includeFinished, 0);
    Untag(Sort(picked))
  }

  // ---------- 一覧の並べ方 ----------
  // 登録した順の位置を添えて並べ、作成日時と位置の組で比べる。位置は重ならないので、並べた結果は 1 通りに決まる

  function Pick(tasks: seq<Task>, includeFinished: bool, from: nat): seq<(nat, Task)>
    decreases |tasks| - from
  {
    if from >= |tasks| then []
    else (if IsVisible(tasks[from], includeFinished) then [(from, tasks[from])] else [])
         + Pick(tasks, includeFinished, from + 1)
  }

  predicate Before(a: (nat, Task), b: (nat, Task))
  {
    a.1.createdAt < b.1.createdAt || (a.1.createdAt == b.1.createdAt && a.0 < b.0)
  }

  predicate Sorted(s: seq<(nat, Task)>)
  {
    forall i, j :: 0 <= i < j < |s| ==> Before(s[i], s[j])
  }

  function Insert(x: (nat, Task), s: seq<(nat, Task)>): (r: seq<(nat, Task)>)
    requires Sorted(s)
    requires forall k :: 0 <= k < |s| ==> s[k].0 != x.0
    ensures Sorted(r)
    ensures multiset(r) == multiset(s) + multiset{x}
  {
    if |s| == 0 then [x]
    else if Before(x, s[0]) then [x] + s
    else
      assert s == [s[0]] + s[1..];
      var rest := Insert(x, s[1..]);
      assert Before(s[0], x);
      assert forall k :: 0 <= k < |rest| ==> rest[k] in multiset(s[1..]) + multiset{x};
      assert forall k :: 0 <= k < |rest| ==> Before(s[0], rest[k]);
      [s[0]] + rest
  }

  function Sort(s: seq<(nat, Task)>): (r: seq<(nat, Task)>)
    requires forall i, j :: 0 <= i < j < |s| ==> s[i].0 != s[j].0
    ensures Sorted(r)
    ensures multiset(r) == multiset(s)
  {
    if |s| == 0 then []
    else
      assert s == [s[0]] + s[1..];
      var rest := Sort(s[1..]);
      assert forall k :: 0 <= k < |rest| ==> rest[k] in multiset(s[1..]);
      Insert(s[0], rest)
  }

  function Untag(s: seq<(nat, Task)>): seq<Task>
  {
    if |s| == 0 then [] else [s[0].1] + Untag(s[1..])
  }

  lemma PickPositions(tasks: seq<Task>, includeFinished: bool, from: nat)
    ensures forall k :: 0 <= k < |Pick(tasks, includeFinished, from)| ==>
      var p := Pick(tasks, includeFinished, from)[k];
      from <= p.0 < |tasks| && p.1 == tasks[p.0] && IsVisible(p.1, includeFinished)
    ensures forall i, j :: 0 <= i < j < |Pick(tasks, includeFinished, from)| ==>
      Pick(tasks, includeFinished, from)[i].0 < Pick(tasks, includeFinished, from)[j].0
    ensures forall p :: from <= p < |tasks| && IsVisible(tasks[p], includeFinished) ==>
      (p, tasks[p]) in Pick(tasks, includeFinished, from)
    decreases |tasks| - from
  {
    if from < |tasks| {
      PickPositions(tasks, includeFinished, from + 1);
    }
  }

  // ---------- 不変条件 ----------

  predicate UniqueIds(st: State)
  {
    forall i, j :: 0 <= i < j < |st.tasks| ==> st.tasks[i].id != st.tasks[j].id
  }

  predicate HasTask(st: State, id: string)
  {
    exists i :: 0 <= i < |st.tasks| && st.tasks[i].id == id
  }

  // イベントの形として正しい
  predicate WellFormedEvent(e: Event)
  {
    !IsBlank(e.actor)
    && match e.from
       case None => e.to == Todo && e.reason.None?
       case Some(f) => !IsFinished(f) && IsFinished(e.to) && e.reason.Some? && !IsBlank(e.reason.value)
  }

  // タスクの経緯: 最初が登録のイベントで、最後のイベントの変更後がいまの状態
  predicate HistoryMatches(t: Task, evs: seq<Event>)
  {
    |evs| >= 1
    && evs[0].from.None?
    && evs[0].at == t.createdAt
    && (forall k :: 1 <= k < |evs| ==> evs[k].from.Some?)
    && (forall k :: 1 <= k < |evs| ==> evs[k].from == Some(evs[k - 1].to))
    && evs[|evs| - 1].to == t.status
  }

  predicate Valid(st: State)
  {
    UniqueIds(st)
    && (forall t :: t in st.tasks ==> !IsBlank(t.title))
    && (forall e :: e in st.events ==> WellFormedEvent(e) && HasTask(st, e.taskId))
    && (forall t :: t in st.tasks ==> HistoryMatches(t, EventsOf(st.events, t.id)))
  }

  // ---------- 性質 ----------

  lemma EmptyIsValid()
    ensures Valid(Empty)
  {
  }

  lemma EventsOfAppend(events: seq<Event>, e: Event, id: string)
    ensures EventsOf(events + [e], id) == EventsOf(events, id) + (if e.taskId == id then [e] else [])
  {
    assert (events + [e])[..|events + [e]| - 1] == events;
  }

  lemma EventsOfMembers(events: seq<Event>, id: string)
    ensures forall e :: e in EventsOf(events, id) ==> e in events && e.taskId == id
  {
    if |events| > 0 {
      EventsOfMembers(events[..|events| - 1], id);
    }
  }

  lemma EventsOfNone(events: seq<Event>, id: string)
    requires forall e :: e in events ==> e.taskId != id
    ensures EventsOf(events, id) == []
  {
    if |events| > 0 {
      EventsOfNone(events[..|events| - 1], id);
    }
  }

  // 登録は不変条件を保つ
  lemma AddPreservesValid(st: State, id: string, title: string, body: string, actor: string, now: UnixMillis)
    requires Valid(st)
    requires Add(st, id, title, body, actor, now).Ok?
    ensures Valid(Add(st, id, title, body, actor, now).value)
  {
    var st' := Add(st, id, title, body, actor, now).value;
    var t := st'.tasks[|st'.tasks| - 1];
    var e := st'.events[|st'.events| - 1];
    assert st'.tasks == st.tasks + [t];
    assert st'.events == st.events + [e];
    EventsOfNone(st.events, id);
    forall u | u in st'.tasks
      ensures HistoryMatches(u, EventsOf(st'.events, u.id))
    {
      EventsOfAppend(st.events, e, u.id);
    }
    forall x | x in st'.events
      ensures HasTask(st', x.taskId)
    {
      if x in st.events {
        var i :| 0 <= i < |st.tasks| && st.tasks[i].id == x.taskId;
        assert st'.tasks[i] == st.tasks[i];
      } else {
        assert st'.tasks[|st'.tasks| - 1].id == x.taskId;
      }
    }
  }

  // 完了・中止は不変条件を保つ
  lemma FinishPreservesValid(st: State, id: string, o: Outcome, reason: string, actor: string, now: UnixMillis)
    requires Valid(st)
    requires FinishTask(st, id, o, reason, actor, now).Ok?
    ensures Valid(FinishTask(st, id, o, reason, actor, now).value)
  {
    var st' := FinishTask(st, id, o, reason, actor, now).value;
    var i := FindIndex(st.tasks, id).value;
    var e := st'.events[|st'.events| - 1];
    assert st'.events == st.events + [e];
    assert e.taskId == id;
    forall u | u in st'.tasks
      ensures HistoryMatches(u, EventsOf(st'.events, u.id))
    {
      EventsOfAppend(st.events, e, u.id);
      var k :| 0 <= k < |st'.tasks| && st'.tasks[k] == u;
      if k == i {
        assert st.tasks[i] in st.tasks;
      } else {
        assert st.tasks[k] in st.tasks;
        assert st.tasks[k].id != id;
      }
    }
    forall x | x in st'.events
      ensures HasTask(st', x.taskId)
    {
      var j :| 0 <= j < |st.tasks| && st.tasks[j].id == x.taskId;
      assert st'.tasks[j].id == st.tasks[j].id;
    }
    forall u | u in st'.tasks
      ensures !IsBlank(u.title)
    {
      var k :| 0 <= k < |st'.tasks| && st'.tasks[k] == u;
      assert st.tasks[k] in st.tasks;
    }
  }

  // 登録したタスクは todo で、ほかのタスクは変わらない
  lemma AddResult(st: State, id: string, title: string, body: string, actor: string, now: UnixMillis)
    requires Add(st, id, title, body, actor, now).Ok?
    ensures var st' := Add(st, id, title, body, actor, now).value;
      st'.tasks == st.tasks + [Task(id, title, body, Todo, now)]
      && st'.events == st.events + [Event(id, actor, None, Todo, None, now)]
  {
  }

  // 登録のエラーの条件
  lemma AddErrors(st: State, id: string, title: string, body: string, actor: string, now: UnixMillis)
    ensures IsBlank(actor) ==> Add(st, id, title, body, actor, now) == Err(SessionRequired)
    ensures !IsBlank(actor) && IsBlank(title) ==> Add(st, id, title, body, actor, now) == Err(TitleRequired)
    ensures !IsBlank(actor) && !IsBlank(title) && HasTask(st, id) ==> Add(st, id, title, body, actor, now) == Err(IdConflict)
    ensures !IsBlank(actor) && !IsBlank(title) && !HasTask(st, id) ==> Add(st, id, title, body, actor, now).Ok?
  {
  }

  // 完了・中止の結果: 対象だけが変わり、イベントが 1 件増える
  lemma FinishResult(st: State, id: string, o: Outcome, reason: string, actor: string, now: UnixMillis)
    requires Valid(st)
    requires FinishTask(st, id, o, reason, actor, now).Ok?
    ensures var st' := FinishTask(st, id, o, reason, actor, now).value;
      |st'.tasks| == |st.tasks|
      && (forall k :: 0 <= k < |st.tasks| ==>
            if st.tasks[k].id == id
            then st'.tasks[k] == st.tasks[k].(status := OutcomeStatus(o))
            else st'.tasks[k] == st.tasks[k])
      && st'.events == st.events + [Event(id, actor, Some(st.tasks[FindIndex(st.tasks, id).value].status), OutcomeStatus(o), Some(reason), now)]
  {
    var i := FindIndex(st.tasks, id).value;
    assert st.tasks[i] in st.tasks;
  }

  // 完了・中止のエラーの条件。並びの順に判定する
  lemma FinishErrors(st: State, id: string, o: Outcome, reason: string, actor: string, now: UnixMillis)
    ensures IsBlank(actor) ==> FinishTask(st, id, o, reason, actor, now) == Err(SessionRequired)
    ensures !IsBlank(actor) && IsBlank(reason) ==> FinishTask(st, id, o, reason, actor, now) == Err(ReasonRequired)
    ensures !IsBlank(actor) && !IsBlank(reason) && !HasTask(st, id) ==> FinishTask(st, id, o, reason, actor, now) == Err(NotFound)
    ensures !IsBlank(actor) && !IsBlank(reason) && HasTask(st, id) ==>
      (FinishTask(st, id, o, reason, actor, now) == Err(AlreadyFinished)
       <==> IsFinished(st.tasks[FindIndex(st.tasks, id).value].status))
  {
    if !IsBlank(actor) && !IsBlank(reason) && HasTask(st, id) {
      var i :| 0 <= i < |st.tasks| && st.tasks[i].id == id;
      assert FindIndex(st.tasks, id).Some?;
    }
  }

  // 終わったタスクは、どの操作でも変わらない
  lemma FinishedTasksNeverChange(st: State, k: nat)
    requires Valid(st)
    requires k < |st.tasks| && IsFinished(st.tasks[k].status)
    ensures forall id, title, body, actor, now :: Add(st, id, title, body, actor, now).Ok? ==>
      Add(st, id, title, body, actor, now).value.tasks[k] == st.tasks[k]
    ensures forall id, o, reason, actor, now :: FinishTask(st, id, o, reason, actor, now).Ok? ==>
      FinishTask(st, id, o, reason, actor, now).value.tasks[k] == st.tasks[k]
  {
    forall id, o, reason, actor, now | FinishTask(st, id, o, reason, actor, now).Ok?
      ensures FinishTask(st, id, o, reason, actor, now).value.tasks[k] == st.tasks[k]
    {
      var i := FindIndex(st.tasks, id).value;
      if i == k {
        assert false;
      }
    }
  }

  // イベントは追記だけ（消えたり書き換わったりしない）
  lemma EventsAppendOnly(st: State, id: string, title: string, body: string, o: Outcome, reason: string, actor: string, now: UnixMillis)
    ensures Add(st, id, title, body, actor, now).Ok? ==>
      var ev := Add(st, id, title, body, actor, now).value.events;
      |ev| == |st.events| + 1 && ev[..|st.events|] == st.events
    ensures FinishTask(st, id, o, reason, actor, now).Ok? ==>
      var ev := FinishTask(st, id, o, reason, actor, now).value.events;
      |ev| == |st.events| + 1 && ev[..|st.events|] == st.events
  {
  }

  // 一覧: 一覧に出すタスクがちょうど 1 回ずつ、作成日時の古い順（同じなら登録した順）に並ぶ
  lemma ListOrdered(st: State, includeFinished: bool)
    requires UniqueIds(st)
    ensures var r := List(st, includeFinished);
      (forall t :: t in r ==> t in st.tasks && IsVisible(t, includeFinished))
      && (forall t :: t in st.tasks && IsVisible(t, includeFinished) ==> t in r)
      && (forall i, j :: 0 <= i < j < |r| ==> r[i].createdAt <= r[j].createdAt)
      && (forall i, j :: 0 <= i < j < |r| && r[i].createdAt == r[j].createdAt ==>
            exists p, q :: 0 <= p < q < |st.tasks| && st.tasks[p] == r[i] && st.tasks[q] == r[j])
      && (forall i, j :: 0 <= i < j < |r| ==> r[i].id != r[j].id)
  {
    var picked := Pick(st.tasks, includeFinished, 0);
    PickPositions(st.tasks, includeFinished, 0);
    var sorted := Sort(picked);
    UntagFacts(sorted);
    SameMembers(picked, sorted);
    var r := List(st, includeFinished);
    assert r == Untag(sorted);
    forall t | t in r ensures t in st.tasks && IsVisible(t, includeFinished) {
      var k :| 0 <= k < |r| && r[k] == t;
      assert sorted[k] in picked;
    }
    forall t | t in st.tasks && IsVisible(t, includeFinished) ensures t in r {
      var p :| 0 <= p < |st.tasks| && st.tasks[p] == t;
      assert (p, t) in sorted;
      var k :| 0 <= k < |sorted| && sorted[k] == (p, t);
      assert r[k] == t;
    }
    forall i, j | 0 <= i < j < |r| ensures r[i].createdAt <= r[j].createdAt {
      assert Before(sorted[i], sorted[j]);
    }
    forall i, j | 0 <= i < j < |r| && r[i].createdAt == r[j].createdAt
      ensures exists p, q :: 0 <= p < q < |st.tasks| && st.tasks[p] == r[i] && st.tasks[q] == r[j]
    {
      assert Before(sorted[i], sorted[j]);
      assert sorted[i] in picked && sorted[j] in picked;
      var p, q := sorted[i].0, sorted[j].0;
      assert st.tasks[p] == r[i] && st.tasks[q] == r[j];
    }
    forall i, j | 0 <= i < j < |r| ensures r[i].id != r[j].id {
      assert Before(sorted[i], sorted[j]);
      assert sorted[i] in picked && sorted[j] in picked;
      if r[i].id == r[j].id {
        var p, q := sorted[i].0, sorted[j].0;
        assert st.tasks[p].id == st.tasks[q].id;
        assert p == q;
        assert sorted[i] == sorted[j];
      }
    }
  }

  lemma SameMembers(a: seq<(nat, Task)>, b: seq<(nat, Task)>)
    requires multiset(a) == multiset(b)
    ensures forall x :: x in a <==> x in b
  {
    forall x ensures x in a <==> x in b {
      assert x in a <==> x in multiset(a);
      assert x in b <==> x in multiset(b);
    }
  }

  lemma UntagFacts(s: seq<(nat, Task)>)
    ensures |Untag(s)| == |s|
    ensures forall k :: 0 <= k < |s| ==> Untag(s)[k] == s[k].1
  {
    if |s| > 0 {
      UntagFacts(s[1..]);
    }
  }
}
