// タスクの状態と操作（#1）
//
// 実装（Go）が呼ぶ関数: NewTask、Finish、ShowTask、List、ValidTask、ErrorCode。IsBlank はテストから呼ぶ（実装では NewTask・Finish の中で使う）。IsVisible は List の中で使う。
// 全体の状態（State）の関数（Empty、Add、FinishTask、Show、Valid）は手本で、実装は同じことを DB で行う。テストで実装と突き合わせるためだけに呼ぶ。
// List だけは実装も呼ぶ。Go が DB から登録した順に読んだタスクの列を、イベントを空にした状態に入れて渡す（ImplListMatchesModel）。
// 読んだ列が DB と一致することは証明ではなく、前提（TasksObserved）。
// 性質ごとの守り方（保証の対応表）は spec/guarantees.toml にある。
// ID と現在時刻、DB で調べた事実（ID が使われているか、ID のタスク）は、仕様の外（Go）で用意し、引数で受け取る。
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

  // from が None なら登録のイベント。登録のイベントに理由がないことは、WellFormedEvent で要求する
  datatype Event = Event(taskId: string, actor: string, from: Option<Status>, to: Status, reason: Option<string>, at: UnixMillis)

  datatype Error =
    | SessionRequired   // 主体（QUENTA_SESSION）がない
    | TitleRequired     // タイトルが空
    | ReasonRequired    // 理由が空
    | NotFound          // その ID のタスクがない
    | AlreadyFinished   // 終わったタスクの状態を変えようとした
    | IdConflict        // 作った ID が既に使われている（実装での扱いは仕様の外。保証の対応表の new-task-id-conflict）

  // エラーコード（CLI がエラーの JSON の code に出す文字列）
  function ErrorCode(e: Error): string
  {
    match e
    case SessionRequired => "session_required"
    case TitleRequired => "title_required"
    case ReasonRequired => "reason_required"
    case NotFound => "not_found"
    case AlreadyFinished => "already_finished"
    case IdConflict => "id_conflict"
  }

  // エラーコードは、エラーごとに違う
  lemma ErrorCodesDistinct(a: Error, b: Error)
    ensures ErrorCode(a) == ErrorCode(b) ==> a == b
  {
  }

  datatype Result<T> = Ok(value: T) | Err(error: Error)

  // 空白とみなす文字。Unicode の文字の性質 White_Space で、Go の unicode.IsSpace と同じ（Go 1.26 の src/unicode/graphic.go と tables.go で照らし合わせた）。
  // 全角の空白（U+3000）を含み、ゼロ幅スペース（U+200B）は含まない。Go と一致することは、テストで確かめる（保証の対応表の is-space）
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

  // ---------- 実装が呼ぶ判定と遷移 ----------

  // 登録。idTaken は、Go が DB で調べた「その ID のタスクが既にあるか」。
  // 主体 → タイトル → ID の順に判定する。タイトルなどは入力のまま保存する（空白を削らない）
  function NewTask(id: string, idTaken: bool, title: string, body: string, actor: string, now: UnixMillis): Result<(Task, Event)>
  {
    if IsBlank(actor) then Err(SessionRequired)
    else if IsBlank(title) then Err(TitleRequired)
    else if idTaken then Err(IdConflict)
    else Ok((Task(id, title, body, Todo, now), Event(id, actor, None, Todo, None, now)))
  }

  // 完了・中止。found は、Go が DB で ID のタスクを探した結果（なければ None）。
  // 主体 → 理由 → タスクがあるか → 終わっているか、の順に判定する
  function Finish(found: Option<Task>, o: Outcome, reason: string, actor: string, now: UnixMillis): Result<(Task, Event)>
  {
    if IsBlank(actor) then Err(SessionRequired)
    else if IsBlank(reason) then Err(ReasonRequired)
    else match found
      case None => Err(NotFound)
      case Some(t) =>
        if IsFinished(t.status) then Err(AlreadyFinished)
        else
          var to := OutcomeStatus(o);
          Ok((t.(status := to), Event(t.id, actor, Some(t.status), to, Some(reason), now)))
  }

  // 詳細。found は、Go が DB で ID のタスクを探した結果（なければ None）
  function ShowTask(found: Option<Task>): Result<Task>
  {
    match found
    case None => Err(NotFound)
    case Some(t) => Ok(t)
  }

  // 一覧に出すか。includeFinished が false なら終わっていないタスクだけ、true ならすべて
  predicate IsVisible(t: Task, includeFinished: bool)
  {
    includeFinished || !IsFinished(t.status)
  }

  // 1 件のタスクの不変条件: タイトルが空でない
  predicate ValidTask(t: Task)
  {
    !IsBlank(t.title)
  }

  // ---------- 実装が呼ぶ関数の性質 ----------

  // 登録のエラーの条件（同値）。返すエラーは、この 3 つのどれか
  lemma NewTaskErrors(id: string, idTaken: bool, title: string, body: string, actor: string, now: UnixMillis)
    ensures NewTask(id, idTaken, title, body, actor, now) == Err(SessionRequired) <==> IsBlank(actor)
    ensures NewTask(id, idTaken, title, body, actor, now) == Err(TitleRequired) <==> !IsBlank(actor) && IsBlank(title)
    ensures NewTask(id, idTaken, title, body, actor, now) == Err(IdConflict) <==> !IsBlank(actor) && !IsBlank(title) && idTaken
    ensures NewTask(id, idTaken, title, body, actor, now).Err? ==>
      NewTask(id, idTaken, title, body, actor, now).error in {SessionRequired, TitleRequired, IdConflict}
  {
  }

  // 登録が成功する条件（同値）と、成功したときの結果: todo のタスクと登録のイベント。
  // タスクは ValidTask を満たし、イベントは形が正しく、そのイベントだけでタスクの経緯が正しい
  lemma NewTaskResult(id: string, idTaken: bool, title: string, body: string, actor: string, now: UnixMillis)
    ensures NewTask(id, idTaken, title, body, actor, now).Ok? <==> !IsBlank(actor) && !IsBlank(title) && !idTaken
    ensures NewTask(id, idTaken, title, body, actor, now).Ok? ==>
      var r := NewTask(id, idTaken, title, body, actor, now).value;
      r.0 == Task(id, title, body, Todo, now)
      && r.1 == Event(id, actor, None, Todo, None, now)
      && ValidTask(r.0)
      && WellFormedEvent(r.1)
      && HistoryMatches(r.0, [r.1])
  {
  }

  // 完了・中止のエラーの条件（同値）。返すエラーは、この 4 つのどれか
  lemma FinishErrors(found: Option<Task>, o: Outcome, reason: string, actor: string, now: UnixMillis)
    ensures Finish(found, o, reason, actor, now) == Err(SessionRequired) <==> IsBlank(actor)
    ensures Finish(found, o, reason, actor, now) == Err(ReasonRequired) <==> !IsBlank(actor) && IsBlank(reason)
    ensures Finish(found, o, reason, actor, now) == Err(NotFound) <==> !IsBlank(actor) && !IsBlank(reason) && found.None?
    ensures Finish(found, o, reason, actor, now) == Err(AlreadyFinished) <==>
      !IsBlank(actor) && !IsBlank(reason) && found.Some? && IsFinished(found.value.status)
    ensures Finish(found, o, reason, actor, now).Err? ==>
      Finish(found, o, reason, actor, now).error in {SessionRequired, ReasonRequired, NotFound, AlreadyFinished}
  {
  }

  // 終わったタスクを渡すと、Finish は必ずエラーを返す
  lemma FinishRejectsFinished(t: Task, o: Outcome, reason: string, actor: string, now: UnixMillis)
    requires IsFinished(t.status)
    ensures Finish(Some(t), o, reason, actor, now).Err?
  {
  }

  // 完了・中止が成功する条件（同値）と、成功したときの結果。
  // - タスクは状態だけが終わらせ方の状態に変わる（ID・タイトル・本文・作成日時は変わらない）
  // - イベントは、変更前がもとの状態、変更後が新しい状態、理由と主体が引数のもので、形が正しい
  // - もとのタスクが ValidTask を満たせば、新しいタスクも満たす
  // - もとのタスクの経緯（イベントの列）が正しければ、そこに返したイベントを足した列で、新しいタスクの経緯も正しい
  lemma FinishResult(found: Option<Task>, o: Outcome, reason: string, actor: string, now: UnixMillis)
    ensures Finish(found, o, reason, actor, now).Ok? <==>
      !IsBlank(actor) && !IsBlank(reason) && found.Some? && !IsFinished(found.value.status)
    ensures Finish(found, o, reason, actor, now).Ok? ==>
      var t := found.value;
      var r := Finish(found, o, reason, actor, now).value;
      r.0 == t.(status := OutcomeStatus(o))
      && r.1 == Event(t.id, actor, Some(t.status), OutcomeStatus(o), Some(reason), now)
      && WellFormedEvent(r.1)
      && (ValidTask(t) ==> ValidTask(r.0))
      && (forall evs :: HistoryMatches(t, evs) ==> HistoryMatches(r.0, evs + [r.1]))
  {
  }

  // 詳細のエラーの条件（同値）と、成功したときの結果: 探したタスクそのもの
  lemma ShowTaskResult(found: Option<Task>)
    ensures ShowTask(found) == Err(NotFound) <==> found.None?
    ensures ShowTask(found).Err? ==> ShowTask(found).error == NotFound
    ensures ShowTask(found).Ok? ==> ShowTask(found).value == found.value
  {
  }

  // ---------- 全体の状態（手本。実装は List のほかはこれらの関数を通らない） ----------

  // tasks は登録した順、events は記録した順
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

  // ID のタスク（なければ None）
  function Lookup(st: State, id: string): Option<Task>
  {
    match FindIndex(st.tasks, id)
    case None => None
    case Some(i) => Some(st.tasks[i])
  }

  // 前提: Go が登録の前に DB で調べた「その ID のタスクが既にあるか」が、DB の状態と一致する
  predicate IdTakenObserved(st: State, id: string, idTaken: bool)
  {
    idTaken == FindIndex(st.tasks, id).Some?
  }

  // 前提: Go が完了・中止・詳細の前に DB で ID のタスクを探した結果が、DB の状態と一致する
  predicate FoundObserved(st: State, id: string, found: Option<Task>)
  {
    found == Lookup(st, id)
  }

  // 前提: Go が一覧の前に DB から読んだタスクの列が、DB の状態のタスクの列（登録した順）と一致する
  predicate TasksObserved(st: State, tasks: seq<Task>)
  {
    tasks == st.tasks
  }

  // 登録（手本）。ID が使われているかを、状態から正しく調べて NewTask に渡す
  function Add(st: State, id: string, title: string, body: string, actor: string, now: UnixMillis): Result<State>
  {
    match NewTask(id, FindIndex(st.tasks, id).Some?, title, body, actor, now)
    case Err(e) => Err(e)
    case Ok((t, e)) => Ok(State(st.tasks + [t], st.events + [e]))
  }

  // 完了・中止（手本）。ID のタスクを、状態から正しく探して Finish に渡す
  function FinishTask(st: State, id: string, o: Outcome, reason: string, actor: string, now: UnixMillis): Result<State>
  {
    match Finish(Lookup(st, id), o, reason, actor, now)
    case Err(e) => Err(e)
    case Ok((t, e)) =>
      FinishOkHasTask(Lookup(st, id), o, reason, actor, now);
      var i := FindIndex(st.tasks, id).value;
      Ok(State(st.tasks[i := t], st.events + [e]))
  }

  // Finish が成功するのは、タスクがあるときだけ
  lemma FinishOkHasTask(found: Option<Task>, o: Outcome, reason: string, actor: string, now: UnixMillis)
    ensures Finish(found, o, reason, actor, now).Ok? ==> found.Some?
  {
  }

  // そのタスクのイベント（記録した順）
  function EventsOf(events: seq<Event>, id: string): seq<Event>
  {
    if |events| == 0 then []
    else EventsOf(events[..|events| - 1], id)
         + (if events[|events| - 1].taskId == id then [events[|events| - 1]] else [])
  }

  // 詳細（手本）。ID のタスクを、状態から正しく探して ShowTask に渡す
  function Show(st: State, id: string): Result<(Task, seq<Event>)>
  {
    match ShowTask(Lookup(st, id))
    case Err(e) => Err(e)
    case Ok(t) => Ok((t, EventsOf(st.events, id)))
  }

  // 前提: DB は、仕様の関数（NewTask・Finish）の結果を、手本（Add・FinishTask）と同じ形で反映する。
  // エラーなら何も変えない。登録が成功なら、タスクとイベントを末尾に 1 件ずつ足す。
  // 完了・中止が成功なら、同じ ID のタスクを返したタスクに置き換え、イベントを末尾に 1 件足す。ほかは変えない
  predicate AppliedLikeModel(st: State, st': State, r: Result<(Task, Event)>, isNew: bool)
  {
    match r
    case Err(_) => st' == st
    case Ok((t, e)) =>
      if isNew then st' == State(st.tasks + [t], st.events + [e])
      else exists i :: 0 <= i < |st.tasks| && st.tasks[i].id == t.id && st' == State(st.tasks[i := t], st.events + [e])
  }

  // Go が渡した「ID が使われているか」が DB と一致すれば（IdTakenObserved）、実装の NewTask の結果は手本の Add が使う結果と同じ
  lemma ImplNewTaskSameResult(st: State, id: string, idTaken: bool, title: string, body: string, actor: string, now: UnixMillis)
    requires IdTakenObserved(st, id, idTaken)
    ensures NewTask(id, idTaken, title, body, actor, now) == NewTask(id, FindIndex(st.tasks, id).Some?, title, body, actor, now)
  {
  }

  // Go が渡した「探したタスク」が DB と一致すれば（FoundObserved）、実装の Finish の結果は手本の FinishTask が使う結果と同じ
  lemma ImplFinishSameResult(st: State, id: string, found: Option<Task>, o: Outcome, reason: string, actor: string, now: UnixMillis)
    requires FoundObserved(st, id, found)
    ensures Finish(found, o, reason, actor, now) == Finish(Lookup(st, id), o, reason, actor, now)
  {
  }

  // 実装の登録の後の DB の状態。Go が渡した「ID が使われているか」が DB と一致し（IdTakenObserved）、
  // DB が NewTask の結果を手本と同じ形で反映すれば（AppliedLikeModel）、
  // 新しい状態は、手本の Add が成功ならその結果、エラーならもとの状態と同じ
  lemma ImplAddMatchesModel(st: State, st': State, id: string, idTaken: bool, title: string, body: string, actor: string, now: UnixMillis)
    requires IdTakenObserved(st, id, idTaken)
    requires AppliedLikeModel(st, st', NewTask(id, idTaken, title, body, actor, now), true)
    ensures Add(st, id, title, body, actor, now).Ok? ==> st' == Add(st, id, title, body, actor, now).value
    ensures Add(st, id, title, body, actor, now).Err? ==> st' == st
  {
  }

  // 実装の完了・中止の後の DB の状態。ID が重ならない状態で、Go が渡した「探したタスク」が DB と一致し（FoundObserved）、
  // DB が Finish の結果を手本と同じ形で反映すれば（AppliedLikeModel）、
  // 新しい状態は、手本の FinishTask が成功ならその結果、エラーならもとの状態と同じ
  lemma ImplFinishMatchesModel(st: State, st': State, id: string, found: Option<Task>, o: Outcome, reason: string, actor: string, now: UnixMillis)
    requires UniqueIds(st)
    requires FoundObserved(st, id, found)
    requires AppliedLikeModel(st, st', Finish(found, o, reason, actor, now), false)
    ensures FinishTask(st, id, o, reason, actor, now).Ok? ==> st' == FinishTask(st, id, o, reason, actor, now).value
    ensures FinishTask(st, id, o, reason, actor, now).Err? ==> st' == st
  {
    if FinishTask(st, id, o, reason, actor, now).Ok? {
      FinishOkHasTask(Lookup(st, id), o, reason, actor, now);
      var r := Finish(found, o, reason, actor, now).value;
      var k := FindIndex(st.tasks, id).value;
      assert r.0.id == id;
      var i :| 0 <= i < |st.tasks| && st.tasks[i].id == r.0.id && st' == State(st.tasks[i := r.0], st.events + [r.1]);
      assert i == k;
    }
  }

  // 実装の詳細。Go が渡した「探したタスク」が DB と一致すれば（FoundObserved）、実装の ShowTask の結果は手本と同じ
  lemma ImplShowMatchesModel(st: State, id: string, found: Option<Task>)
    requires FoundObserved(st, id, found)
    ensures ShowTask(found) == ShowTask(Lookup(st, id))
  {
  }

  // 実装の一覧。List は全体の状態（タスクの列とイベントの列）を受け取るが、実装は一覧のためにイベントを読まず、
  // DB から読んだタスクの列に、空のイベントの列を組み合わせた状態を渡す。この補題は、その状態で呼んだ List の結果が、
  // DB の本当の状態で呼んだ List の結果と同じことを言う。前提は、Go が読んだタスクの列が DB と一致すること（TasksObserved）。
  // ListOrdered は、ID が重ならない DB の本当の状態での List についての性質なので、ID が重ならなければ、これで実装の一覧の性質になる。
  // イベントのないタスクを扱う補題ではない（イベントの列を空にするのは、List が使わない入力を埋めるため）
  lemma ImplListMatchesModel(st: State, tasks: seq<Task>, includeFinished: bool)
    requires TasksObserved(st, tasks)
    ensures List(State(tasks, []), includeFinished) == List(st, includeFinished)
  {
  }

  // 実装の登録は、DB の状態の不変条件を保つ（帰納法の一歩）。前提は ImplAddMatchesModel と同じ
  lemma ImplAddPreservesValid(st: State, st': State, id: string, idTaken: bool, title: string, body: string, actor: string, now: UnixMillis)
    requires Valid(st)
    requires IdTakenObserved(st, id, idTaken)
    requires AppliedLikeModel(st, st', NewTask(id, idTaken, title, body, actor, now), true)
    ensures Valid(st')
  {
    ImplAddMatchesModel(st, st', id, idTaken, title, body, actor, now);
    if Add(st, id, title, body, actor, now).Ok? {
      AddPreservesValid(st, id, title, body, actor, now);
    }
  }

  // 実装の完了・中止は、DB の状態の不変条件を保つ（帰納法の一歩）。前提は ImplFinishMatchesModel と同じ
  lemma ImplFinishPreservesValid(st: State, st': State, id: string, found: Option<Task>, o: Outcome, reason: string, actor: string, now: UnixMillis)
    requires Valid(st)
    requires FoundObserved(st, id, found)
    requires AppliedLikeModel(st, st', Finish(found, o, reason, actor, now), false)
    ensures Valid(st')
  {
    ImplFinishMatchesModel(st, st', id, found, o, reason, actor, now);
    if FinishTask(st, id, o, reason, actor, now).Ok? {
      FinishPreservesValid(st, id, o, reason, actor, now);
    }
  }

  // 一覧: 一覧に出すタスクを、作成日時の古い順に並べる。作成日時が同じなら登録した順
  function List(st: State, includeFinished: bool): seq<Task>
  {
    var picked := Pick(st.tasks, includeFinished, 0);
    PickPositions(st.tasks, includeFinished, 0);
    Untag(Sort(picked))
  }

  // ---------- 一覧の並べ方（List の中で使う。実装も通る） ----------
  // 登録した順の位置を添えて並べ、作成日時と位置の組で比べる

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

  // どの 2 つのタスクも ID が違う
  predicate UniqueIds(st: State)
  {
    forall i, j :: 0 <= i < j < |st.tasks| ==> st.tasks[i].id != st.tasks[j].id
  }

  predicate HasTask(st: State, id: string)
  {
    exists i :: 0 <= i < |st.tasks| && st.tasks[i].id == id
  }

  // イベントの形として正しい: 主体が空でない。登録のイベントは変更後が todo で理由がない。
  // 変更のイベントは、変更前が終わっていない状態、変更後が終わった状態で、空でない理由がある
  predicate WellFormedEvent(e: Event)
  {
    !IsBlank(e.actor)
    && match e.from
       case None => e.to == Todo && e.reason.None?
       case Some(f) => !IsFinished(f) && IsFinished(e.to) && e.reason.Some? && !IsBlank(e.reason.value)
  }

  // タスクの経緯: イベントが 1 件以上あり、最初が登録のイベントで、その日時は作成日時と同じ。
  // 2 件目以降は変更のイベントで、変更前は直前のイベントの変更後と同じ。最後のイベントの変更後がいまの状態
  predicate HistoryMatches(t: Task, evs: seq<Event>)
  {
    |evs| >= 1
    && evs[0].from.None?
    && evs[0].at == t.createdAt
    && (forall k :: 1 <= k < |evs| ==> evs[k].from.Some?)
    && (forall k :: 1 <= k < |evs| ==> evs[k].from == Some(evs[k - 1].to))
    && evs[|evs| - 1].to == t.status
  }

  // 全体の不変条件: ID が重ならない、タイトルが空でない、イベントの形が正しく対象のタスクがある、どのタスクも経緯が正しい。
  // 実装では「DB の状態がこれを満たす」ことを前提にする（DB が、証明した操作の結果だけで変わることによる）
  predicate Valid(st: State)
  {
    UniqueIds(st)
    && (forall t :: t in st.tasks ==> ValidTask(t))
    && (forall e :: e in st.events ==> WellFormedEvent(e) && HasTask(st, e.taskId))
    && (forall t :: t in st.tasks ==> HistoryMatches(t, EventsOf(st.events, t.id)))
  }

  // ---------- 全体の状態の性質 ----------

  // 空の状態は不変条件を満たす（帰納法の出発点）。実装では、新しく作った DB が空（Empty）であることを前提にする。
  // それは証明ではなく、人のレビューとテストで守る（保証の対応表の empty-is-valid）
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
    assert t.id == id && e.taskId == id;
    assert FindIndex(st.tasks, id).None?;
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
    FinishOkHasTask(Lookup(st, id), o, reason, actor, now);
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
      ensures ValidTask(u)
    {
      var k :| 0 <= k < |st'.tasks| && st'.tasks[k] == u;
      assert st.tasks[k] in st.tasks;
    }
  }

  // 登録の結果（手本）: 登録したタスクとイベントが末尾に 1 件ずつ増え、既存のものは変わらない
  lemma AddResult(st: State, id: string, title: string, body: string, actor: string, now: UnixMillis)
    requires Add(st, id, title, body, actor, now).Ok?
    ensures var st' := Add(st, id, title, body, actor, now).value;
      st'.tasks == st.tasks + [Task(id, title, body, Todo, now)]
      && st'.events == st.events + [Event(id, actor, None, Todo, None, now)]
  {
  }

  // 完了・中止の結果（手本）: 対象のタスクだけが変わり、イベントが末尾に 1 件増える
  lemma FinishTaskResult(st: State, id: string, o: Outcome, reason: string, actor: string, now: UnixMillis)
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
    FinishOkHasTask(Lookup(st, id), o, reason, actor, now);
    var i := FindIndex(st.tasks, id).value;
    assert st.tasks[i] in st.tasks;
  }

  // 終わったタスクは、どの操作でも変わらない（手本）
  lemma FinishedTasksNeverChange(st: State, k: nat)
    requires k < |st.tasks| && IsFinished(st.tasks[k].status)
    ensures forall id, title, body, actor, now :: Add(st, id, title, body, actor, now).Ok? ==>
      Add(st, id, title, body, actor, now).value.tasks[k] == st.tasks[k]
    ensures forall id, o, reason, actor, now :: FinishTask(st, id, o, reason, actor, now).Ok? ==>
      FinishTask(st, id, o, reason, actor, now).value.tasks[k] == st.tasks[k]
  {
    forall id, o, reason, actor, now | FinishTask(st, id, o, reason, actor, now).Ok?
      ensures FinishTask(st, id, o, reason, actor, now).value.tasks[k] == st.tasks[k]
    {
      FinishOkHasTask(Lookup(st, id), o, reason, actor, now);
      var i := FindIndex(st.tasks, id).value;
      if i == k {
        assert false;
      }
    }
  }

  // タスクは消えない（手本）: 登録でも完了・中止でも、既存のタスクは同じ位置（登録した順）に同じ ID で残る
  lemma TasksNeverDisappear(st: State, id: string, title: string, body: string, o: Outcome, reason: string, actor: string, now: UnixMillis)
    ensures Add(st, id, title, body, actor, now).Ok? ==>
      var ts := Add(st, id, title, body, actor, now).value.tasks;
      |ts| == |st.tasks| + 1 && forall k :: 0 <= k < |st.tasks| ==> ts[k].id == st.tasks[k].id
    ensures FinishTask(st, id, o, reason, actor, now).Ok? ==>
      var ts := FinishTask(st, id, o, reason, actor, now).value.tasks;
      |ts| == |st.tasks| && forall k :: 0 <= k < |st.tasks| ==> ts[k].id == st.tasks[k].id
  {
    if FinishTask(st, id, o, reason, actor, now).Ok? {
      FinishOkHasTask(Lookup(st, id), o, reason, actor, now);
    }
  }

  // イベントは追記だけ（手本）: 登録でも完了・中止でも、既存のイベントは消えず書き換わらず、末尾に 1 件増える
  lemma EventsAppendOnly(st: State, id: string, title: string, body: string, o: Outcome, reason: string, actor: string, now: UnixMillis)
    ensures Add(st, id, title, body, actor, now).Ok? ==>
      var ev := Add(st, id, title, body, actor, now).value.events;
      |ev| == |st.events| + 1 && ev[..|st.events|] == st.events
    ensures FinishTask(st, id, o, reason, actor, now).Ok? ==>
      var ev := FinishTask(st, id, o, reason, actor, now).value.events;
      |ev| == |st.events| + 1 && ev[..|st.events|] == st.events
  {
  }

  // 一覧: ID が重ならない状態で、一覧に出すタスクがちょうど 1 回ずつ、作成日時の古い順（同じなら登録した順）に並ぶ。
  // 全体を一度に証明させると、証明器の乱数の種によって数秒から 2 分以上までばらつき、CI で時間切れになった（#16）。
  // 主張ごとに分けると、どの種でも 1 件 0.3 秒以内で終わる
  lemma {:isolate_assertions} ListOrdered(st: State, includeFinished: bool)
    requires UniqueIds(st)
    ensures forall t :: t in List(st, includeFinished) ==> t in st.tasks && IsVisible(t, includeFinished)
    ensures forall t :: t in st.tasks && IsVisible(t, includeFinished) ==> t in List(st, includeFinished)
    ensures forall i, j :: 0 <= i < j < |List(st, includeFinished)| ==> List(st, includeFinished)[i].id != List(st, includeFinished)[j].id
    ensures forall i, j :: 0 <= i < j < |List(st, includeFinished)| ==>
      List(st, includeFinished)[i].createdAt <= List(st, includeFinished)[j].createdAt
    ensures forall i, j :: 0 <= i < j < |List(st, includeFinished)| && List(st, includeFinished)[i].createdAt == List(st, includeFinished)[j].createdAt ==>
      exists p, q :: 0 <= p < q < |st.tasks| && st.tasks[p] == List(st, includeFinished)[i] && st.tasks[q] == List(st, includeFinished)[j]
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
