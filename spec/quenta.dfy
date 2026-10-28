// quenta のモデルの層。念入りにした機能（登録・一覧・完了・中止）の状態と操作の判定・遷移
// 操作を 2 つに分けて書く。判定（Decide）は、DB から問い合わせた事実と入力から、エラーか、書き込む内容（一覧では返すタスク）を決める。
// DB のモデルは、状態の全体と、問い合わせ（Query）と書き込み（Apply）を持つ。Add などは、問い合わせ → 判定 → 書き込み をつないだもの
// テストの中でだけ Go に変換し、実装と突き合わせるオラクルにする（CLAUDE.md の「モデルベーステストとプロパティベーステスト」）

module Quenta {

  // ---- 値 ----

  // @assurance:internal 値がないことを表す、仕様の中だけで使う型
  datatype Option<T> = None | Some(value: T)

  // @assurance:subject task-id
  type TaskId = string

  // @assurance:internal 日時。1970-01-01T00:00:00Z からの秒の数
  type Time = int

  // @assurance:subject task-status
  datatype Status = Todo | Done | Canceled

  // @assurance:subject task
  datatype Task = Task(id: TaskId, title: string, body: string, status: Status, createdAt: Time)

  // @assurance:subject event
  datatype Event = Event(taskId: TaskId, actor: string, from: Option<Status>, to: Status, reason: string, at: Time)

  // 書き込みは、イベントを events の末尾に足す。1 つのタスクのイベントの順は、events の中の順（EventsOf）で表す
  // @assurance:internal DB のモデルの状態。データのファイルの中のタスクとイベントの全体
  datatype State = State(tasks: seq<Task>, events: seq<Event>)

  // @assurance:internal モデルが返すエラー。エラーコードへの写し方は仕様の外で、主張 cli-model-error-code が決める
  datatype Error = SessionRequired | TitleRequired | ReasonRequired | NotFound | AlreadyFinished | IdTaken

  // @assurance:internal 操作の結果。成功なら値を、失敗ならエラーを持つ
  datatype Result<T> = Ok(value: T) | Err(error: Error)

  const Empty := State([], [])

  // Unicode の White_Space の性質を持つ文字（Unicode 15.1 の PropList.txt）
  predicate IsWhiteSpace(c: char) {
    ('\U{0009}' <= c <= '\U{000D}') || c == '\U{0020}' || c == '\U{0085}' || c == '\U{00A0}' || c == '\U{1680}'
    || ('\U{2000}' <= c <= '\U{200A}') || c == '\U{2028}' || c == '\U{2029}' || c == '\U{202F}' || c == '\U{205F}'
    || c == '\U{3000}'
  }

  // @assurance:subject blank
  predicate IsBlank(s: string) {
    forall i :: 0 <= i < |s| ==> IsWhiteSpace(s[i])
  }

  // 終わった状態を、done と canceled を挙げて決める（状態が増えても、挙げていない状態は終わった状態にならない）
  predicate IsFinished(st: Status) {
    st == Done || st == Canceled
  }

  // ---- DB のモデル: 問い合わせ ----

  function FindIndex(tasks: seq<Task>, id: TaskId): Option<nat>
    ensures FindIndex(tasks, id).Some? ==> FindIndex(tasks, id).value < |tasks| && tasks[FindIndex(tasks, id).value].id == id
    ensures FindIndex(tasks, id).None? <==> forall i :: 0 <= i < |tasks| ==> tasks[i].id != id
  {
    if |tasks| == 0 then None
    else if tasks[0].id == id then Some(0)
    else match FindIndex(tasks[1..], id)
      case None => None
      case Some(i) => Some(i + 1)
  }

  predicate IsIdTaken(s: State, id: TaskId) {
    exists i :: 0 <= i < |s.tasks| && s.tasks[i].id == id
  }

  // 登録の判定に渡す事実: その ID のタスクがもうあるか
  function QueryIdTaken(s: State, id: TaskId): bool {
    IsIdTaken(s, id)
  }

  // 完了・中止の判定に渡す事実: その ID のタスク（なければ None）
  function QueryTask(s: State, id: TaskId): Option<Task> {
    match FindIndex(s.tasks, id)
      case None => None
      case Some(i) => Some(s.tasks[i])
  }

  // 一覧の判定に渡す事実: タスクの全体
  function QueryTasks(s: State): seq<Task> {
    s.tasks
  }

  // 1 つのタスクのイベントを、記録の順に並べたもの
  function EventsOf(events: seq<Event>, id: TaskId): seq<Event> {
    if |events| == 0 then []
    else EventsOf(events[..|events| - 1], id) + (if events[|events| - 1].taskId == id then [events[|events| - 1]] else [])
  }

  // ---- 不変条件 ----

  // ID が重ならず、どのタスクにもイベントがあり、状態がそのタスクの最後のイベントの変更後と一致する
  // @assurance:claim task-id-unique
  // @assurance:claim status-matches-last-event
  predicate Inv(s: State) {
    (forall i, j :: 0 <= i < j < |s.tasks| ==> s.tasks[i].id != s.tasks[j].id)
    && (forall i :: 0 <= i < |s.tasks| ==>
          |EventsOf(s.events, s.tasks[i].id)| > 0
          && EventsOf(s.events, s.tasks[i].id)[|EventsOf(s.events, s.tasks[i].id)| - 1].to == s.tasks[i].status)
  }

  // ---- 判定 ----

  // @assurance:internal 登録で書き込む内容。足すタスクとイベント
  datatype AddWrite = AddWrite(task: Task, event: Event)

  // @assurance:internal 完了・中止で書き込む内容。状態を変えるタスクの ID、変更後の状態、足すイベント
  datatype FinishWrite = FinishWrite(id: TaskId, status: Status, event: Event)

  // 登録の判定。idTaken は、その ID のタスクがもうあるか（DB に問い合わせた事実）
  function AddDecide(actor: string, title: string, body: string, id: TaskId, now: Time, idTaken: bool): Result<AddWrite> {
    if IsBlank(actor) then Err(SessionRequired)
    else if IsBlank(title) then Err(TitleRequired)
    else if idTaken then Err(IdTaken)
    else Ok(AddWrite(Task(id, title, body, Todo, now), Event(id, actor, None, Todo, "", now)))
  }

  // 完了か中止か。行き先の状態は Target で決まり、どちらも終わった状態
  // @assurance:internal 完了と中止を見分ける、仕様の中だけで使う型
  datatype FinishKind = Completing | Canceling

  function Target(k: FinishKind): Status {
    match k
      case Completing => Done
      case Canceling => Canceled
  }

  // 完了・中止の判定。found は、指定した ID のタスク（DB に問い合わせた事実。なければ None）
  function FinishDecide(actor: string, id: TaskId, reason: string, k: FinishKind, now: Time, found: Option<Task>): Result<FinishWrite> {
    if IsBlank(actor) then Err(SessionRequired)
    else if IsBlank(reason) then Err(ReasonRequired)
    else match found
      case None => Err(NotFound)
      case Some(t) =>
        if IsFinished(t.status) then Err(AlreadyFinished)
        else Ok(FinishWrite(id, Target(k), Event(id, actor, Some(t.status), Target(k), reason, now)))
  }

  // ---- DB のモデル: 書き込み ----

  // 登録の書き込みが前提にすること: その ID のタスクがまだなく、足すタスクの状態が todo で、イベントがそのタスクの、変更前なし・変更後 todo のもの
  predicate AddWritable(s: State, w: AddWrite) {
    !IsIdTaken(s, w.task.id) && w.task.status == Todo
    && w.event.taskId == w.task.id && w.event.from == None && w.event.to == Todo
  }

  // タスクとイベントを末尾に足すほかは、何も変えない
  function ApplyAdd(s: State, w: AddWrite): State
    requires AddWritable(s, w)
  {
    State(s.tasks + [w.task], s.events + [w.event])
  }

  // 完了・中止の書き込みが前提にすること: その ID のタスクがあって終わっておらず、行き先が終わった状態で、イベントがそのタスクの、変更前がもとの状態・変更後が行き先のもの
  predicate FinishWritable(s: State, w: FinishWrite) {
    FindIndex(s.tasks, w.id).Some?
    && !IsFinished(s.tasks[FindIndex(s.tasks, w.id).value].status) && IsFinished(w.status)
    && w.event.taskId == w.id && w.event.from == Some(s.tasks[FindIndex(s.tasks, w.id).value].status) && w.event.to == w.status
  }

  // 指定した ID のタスクの状態だけを変え、イベントを末尾に足すほかは、何も変えない
  function ApplyFinish(s: State, w: FinishWrite): State
    requires FinishWritable(s, w)
  {
    var i := FindIndex(s.tasks, w.id).value;
    State(s.tasks[i := s.tasks[i].(status := w.status)], s.events + [w.event])
  }

  // ---- 操作（問い合わせ → 判定 → 書き込み） ----

  // @assurance:subject task-add
  function Add(s: State, actor: string, title: string, body: string, id: TaskId, now: Time): Result<State> {
    match AddDecide(actor, title, body, id, now, QueryIdTaken(s, id))
      case Err(e) => Err(e)
      case Ok(w) =>
        AddDecideWritable(s, actor, title, body, id, now);
        Ok(ApplyAdd(s, w))
  }

  // 判定が、問い合わせた事実から書き込む内容を返したなら、その書き込みの前提は満たされる
  lemma AddDecideWritable(s: State, actor: string, title: string, body: string, id: TaskId, now: Time)
    ensures AddDecide(actor, title, body, id, now, QueryIdTaken(s, id)).Ok? ==> AddWritable(s, AddDecide(actor, title, body, id, now, QueryIdTaken(s, id)).value)
  {}

  function Finish(s: State, actor: string, id: TaskId, reason: string, k: FinishKind, now: Time): Result<State> {
    match FinishDecide(actor, id, reason, k, now, QueryTask(s, id))
      case Err(e) => Err(e)
      case Ok(w) =>
        FinishDecideWritable(s, actor, id, reason, k, now);
        Ok(ApplyFinish(s, w))
  }

  // 判定が、問い合わせた事実から書き込む内容を返したなら、その書き込みの前提は満たされる
  lemma FinishDecideWritable(s: State, actor: string, id: TaskId, reason: string, k: FinishKind, now: Time)
    ensures FinishDecide(actor, id, reason, k, now, QueryTask(s, id)).Ok? ==> FinishWritable(s, FinishDecide(actor, id, reason, k, now, QueryTask(s, id)).value)
  {}

  // @assurance:subject task-done
  function Complete(s: State, actor: string, id: TaskId, reason: string, now: Time): Result<State> {
    Finish(s, actor, id, reason, Completing, now)
  }

  // @assurance:subject task-cancel
  function Cancel(s: State, actor: string, id: TaskId, reason: string, now: Time): Result<State> {
    Finish(s, actor, id, reason, Canceling, now)
  }

  // 一覧の並び: 作成日時の古い順、同じなら ID の昇順（文字の符号位置で比べる辞書式の順）
  predicate IdLess(a: string, b: string) {
    if |a| == 0 then |b| > 0
    else if |b| == 0 then false
    else a[0] < b[0] || (a[0] == b[0] && IdLess(a[1..], b[1..]))
  }

  predicate Before(a: Task, b: Task) {
    a.createdAt < b.createdAt || (a.createdAt == b.createdAt && IdLess(a.id, b.id))
  }

  predicate Sorted(xs: seq<Task>) {
    forall i, j :: 0 <= i < j < |xs| ==> Before(xs[i], xs[j])
  }

  function Insert(t: Task, xs: seq<Task>): seq<Task> {
    if |xs| == 0 then [t]
    else if Before(t, xs[0]) then [t] + xs
    else [xs[0]] + Insert(t, xs[1..])
  }

  function Sort(xs: seq<Task>): seq<Task> {
    if |xs| == 0 then [] else Insert(xs[0], Sort(xs[1..]))
  }

  function Select(tasks: seq<Task>, includeFinished: bool): seq<Task> {
    if |tasks| == 0 then []
    else (if includeFinished || !IsFinished(tasks[0].status) then [tasks[0]] else []) + Select(tasks[1..], includeFinished)
  }

  // 一覧の判定。tasks は、DB に問い合わせたタスクの集まり
  function ListDecide(actor: string, includeFinished: bool, tasks: seq<Task>): Result<seq<Task>> {
    if IsBlank(actor) then Err(SessionRequired)
    else Ok(Sort(Select(tasks, includeFinished)))
  }

  // 一覧は書き込みを持たず、状態を返さないので、状態を変えない
  // @assurance:subject task-list
  // @assurance:claim model-changes-only-target
  function List(s: State, actor: string, includeFinished: bool): Result<seq<Task>> {
    ListDecide(actor, includeFinished, QueryTasks(s))
  }

  // ---- 登録の判定の補題 ----

  // @assurance:claim session-required
  // @assurance:claim model-error-precedence
  // @assurance:claim model-errors-only-when
  lemma AddSessionRequired(actor: string, title: string, body: string, id: TaskId, now: Time, idTaken: bool)
    ensures AddDecide(actor, title, body, id, now, idTaken) == Err(SessionRequired) <==> IsBlank(actor)
  {}

  // @assurance:claim task-add-title-required
  // @assurance:claim model-error-precedence
  // @assurance:claim model-errors-only-when
  lemma AddTitleRequired(actor: string, title: string, body: string, id: TaskId, now: Time, idTaken: bool)
    ensures AddDecide(actor, title, body, id, now, idTaken) == Err(TitleRequired) <==> !IsBlank(actor) && IsBlank(title)
  {}

  // @assurance:claim task-add-id-taken
  // @assurance:claim model-error-precedence
  // @assurance:claim model-errors-only-when
  lemma AddIdTaken(actor: string, title: string, body: string, id: TaskId, now: Time, idTaken: bool)
    ensures AddDecide(actor, title, body, id, now, idTaken) == Err(IdTaken) <==> !IsBlank(actor) && !IsBlank(title) && idTaken
  {}

  // @assurance:claim model-errors-only-when
  lemma AddOnlyThoseErrors(actor: string, title: string, body: string, id: TaskId, now: Time, idTaken: bool)
    ensures AddDecide(actor, title, body, id, now, idTaken).Err? ==> AddDecide(actor, title, body, id, now, idTaken).error in {SessionRequired, TitleRequired, IdTaken}
    ensures AddDecide(actor, title, body, id, now, idTaken).Ok? <==> !IsBlank(actor) && !IsBlank(title) && !idTaken
  {}

  // エラーの条件に当てはまらなければ成功し、書き込む内容は、渡された値のままのタスク（状態 todo）と、変更前なし・理由が空のイベント
  // @assurance:claim task-add-saves-todo
  // @assurance:claim task-add-records-event
  // @assurance:claim event-actor-as-given
  lemma AddSucceeds(actor: string, title: string, body: string, id: TaskId, now: Time, idTaken: bool)
    requires !IsBlank(actor) && !IsBlank(title) && !idTaken
    ensures AddDecide(actor, title, body, id, now, idTaken)
            == Ok(AddWrite(Task(id, title, body, Todo, now), Event(id, actor, None, Todo, "", now)))
  {}

  // 本文が何であっても、成功するかどうかと、失敗したときのエラーは変わらない。成功すれば、本文はそのまま入る
  // @assurance:claim task-add-empty-body
  lemma AddBodyNeverRejected(actor: string, title: string, body: string, other: string, id: TaskId, now: Time, idTaken: bool)
    ensures AddDecide(actor, title, body, id, now, idTaken).Ok? <==> AddDecide(actor, title, other, id, now, idTaken).Ok?
    ensures AddDecide(actor, title, body, id, now, idTaken).Err? ==> AddDecide(actor, title, body, id, now, idTaken) == AddDecide(actor, title, other, id, now, idTaken)
    ensures AddDecide(actor, title, body, id, now, idTaken).Ok? ==> AddDecide(actor, title, body, id, now, idTaken).value.task.body == body
  {}

  // ---- 登録の操作の補題（問い合わせ → 判定 → 書き込み） ----

  // 成功したときの状態は、もとの状態の末尾にタスクを 1 つとイベントを 1 件足したもので、ほかは同じ
  // @assurance:claim model-changes-only-target
  // @assurance:claim task-add-saves-todo
  // @assurance:claim task-add-records-event
  lemma AddChangesOnlyTarget(s: State, actor: string, title: string, body: string, id: TaskId, now: Time)
    ensures Add(s, actor, title, body, id, now).Ok? ==>
            Add(s, actor, title, body, id, now).value.tasks == s.tasks + [Task(id, title, body, Todo, now)]
            && Add(s, actor, title, body, id, now).value.events == s.events + [Event(id, actor, None, Todo, "", now)]
  {}

  // @assurance:claim task-id-unique
  // @assurance:claim status-matches-last-event
  lemma AddKeepsInv(s: State, actor: string, title: string, body: string, id: TaskId, now: Time)
    requires Inv(s)
    ensures Add(s, actor, title, body, id, now).Ok? ==> Inv(Add(s, actor, title, body, id, now).value)
  {
    if Add(s, actor, title, body, id, now).Ok? {
      var s' := Add(s, actor, title, body, id, now).value;
      var e := Event(id, actor, None, Todo, "", now);
      forall i | 0 <= i < |s'.tasks|
        ensures |EventsOf(s'.events, s'.tasks[i].id)| > 0
        ensures EventsOf(s'.events, s'.tasks[i].id)[|EventsOf(s'.events, s'.tasks[i].id)| - 1].to == s'.tasks[i].status
      {
        EventsOfAppend(s.events, e, s'.tasks[i].id);
        if i < |s.tasks| {
          assert s.tasks[i].id != id;
        }
      }
    }
  }

  // ---- 完了・中止の判定の補題 ----
  // FinishDecide の補題は、完了（Completing。行き先 done）と中止（Canceling。行き先 canceled）の両方に当てはまる

  // @assurance:claim session-required
  // @assurance:claim model-error-precedence
  // @assurance:claim model-errors-only-when
  lemma FinishSessionRequired(actor: string, id: TaskId, reason: string, k: FinishKind, now: Time, found: Option<Task>)
    ensures FinishDecide(actor, id, reason, k, now, found) == Err(SessionRequired) <==> IsBlank(actor)
  {}

  // @assurance:claim finish-reason-required
  // @assurance:claim model-error-precedence
  // @assurance:claim model-errors-only-when
  lemma FinishReasonRequired(actor: string, id: TaskId, reason: string, k: FinishKind, now: Time, found: Option<Task>)
    ensures FinishDecide(actor, id, reason, k, now, found) == Err(ReasonRequired) <==> !IsBlank(actor) && IsBlank(reason)
  {}

  // @assurance:claim task-not-found
  // @assurance:claim model-error-precedence
  // @assurance:claim model-errors-only-when
  lemma FinishNotFound(actor: string, id: TaskId, reason: string, k: FinishKind, now: Time, found: Option<Task>)
    ensures FinishDecide(actor, id, reason, k, now, found) == Err(NotFound) <==> !IsBlank(actor) && !IsBlank(reason) && found.None?
  {}

  // @assurance:claim finished-task-unchangeable
  // @assurance:claim model-error-precedence
  // @assurance:claim model-errors-only-when
  lemma FinishAlreadyFinished(actor: string, id: TaskId, reason: string, k: FinishKind, now: Time, found: Option<Task>)
    ensures FinishDecide(actor, id, reason, k, now, found) == Err(AlreadyFinished)
            <==> !IsBlank(actor) && !IsBlank(reason) && found.Some? && IsFinished(found.value.status)
  {}

  // @assurance:claim model-errors-only-when
  lemma FinishOnlyThoseErrors(actor: string, id: TaskId, reason: string, k: FinishKind, now: Time, found: Option<Task>)
    ensures FinishDecide(actor, id, reason, k, now, found).Err? ==> FinishDecide(actor, id, reason, k, now, found).error in {SessionRequired, ReasonRequired, NotFound, AlreadyFinished}
    ensures FinishDecide(actor, id, reason, k, now, found).Ok?
            <==> !IsBlank(actor) && !IsBlank(reason) && found.Some? && !IsFinished(found.value.status)
  {}

  // エラーの条件に当てはまらなければ成功し、書き込む内容は、指定した ID のタスクの状態を行き先にすることと、変更前 todo のイベント
  // @assurance:claim task-done-sets-done
  // @assurance:claim task-cancel-sets-canceled
  // @assurance:claim event-actor-as-given
  lemma FinishSucceeds(actor: string, id: TaskId, reason: string, k: FinishKind, now: Time, found: Option<Task>)
    requires !IsBlank(actor) && !IsBlank(reason) && found.Some? && found.value.status == Todo
    ensures FinishDecide(actor, id, reason, k, now, found)
            == Ok(FinishWrite(id, Target(k), Event(id, actor, Some(Todo), Target(k), reason, now)))
  {}

  // 完了は、行き先を done とした Finish
  // @assurance:claim task-done-sets-done
  lemma CompleteIsFinishToDone(s: State, actor: string, id: TaskId, reason: string, now: Time)
    ensures Complete(s, actor, id, reason, now) == Finish(s, actor, id, reason, Completing, now)
    ensures Target(Completing) == Done
  {}

  // 中止は、行き先を canceled とした Finish
  // @assurance:claim task-cancel-sets-canceled
  lemma CancelIsFinishToCanceled(s: State, actor: string, id: TaskId, reason: string, now: Time)
    ensures Cancel(s, actor, id, reason, now) == Finish(s, actor, id, reason, Canceling, now)
    ensures Target(Canceling) == Canceled
  {}

  // ---- 完了・中止の操作の補題（問い合わせ → 判定 → 書き込み） ----

  // 問い合わせが「なし」を返すのは、状態にその ID のタスクがないときに限る。返すのは状態の中のそのタスク（主張 task-not-found の「見つからない」と、状態とのつながり）
  // @assurance:claim task-not-found
  // @assurance:claim finished-task-unchangeable
  lemma QueryTaskFinds(s: State, id: TaskId)
    ensures QueryTask(s, id).Some? <==> IsIdTaken(s, id)
    ensures QueryTask(s, id).Some? ==> QueryTask(s, id).value == s.tasks[FindIndex(s.tasks, id).value] && QueryTask(s, id).value.id == id
  {}

  // 成功したなら、もとの状態は todo で、成功したときの状態は、指定したタスクの状態だけを行き先に変え、変更前 todo のイベントを 1 件足したもので、ほかは同じ
  // @assurance:claim model-changes-only-target
  // @assurance:claim task-done-sets-done
  // @assurance:claim task-cancel-sets-canceled
  lemma FinishChangesOnlyTarget(s: State, actor: string, id: TaskId, reason: string, k: FinishKind, now: Time)
    ensures Finish(s, actor, id, reason, k, now).Ok? ==>
            var i := FindIndex(s.tasks, id).value;
            s.tasks[i].status == Todo
            && Finish(s, actor, id, reason, k, now).value.tasks == s.tasks[i := s.tasks[i].(status := Target(k))]
            && Finish(s, actor, id, reason, k, now).value.events == s.events + [Event(id, actor, Some(Todo), Target(k), reason, now)]
  {}

  // 終わったタスクは、登録・完了・中止のどれが成功した後も、同じ値のまま状態に残る（失敗したときは状態を返さない）
  // @assurance:claim finished-task-preserved
  lemma FinishedTaskPreserved(s: State, t: Task, actor: string, title: string, body: string, id: TaskId, reason: string, k: FinishKind, now: Time)
    requires t in s.tasks && IsFinished(t.status)
    ensures Add(s, actor, title, body, id, now).Ok? ==> t in Add(s, actor, title, body, id, now).value.tasks
    ensures Finish(s, actor, id, reason, k, now).Ok? ==> t in Finish(s, actor, id, reason, k, now).value.tasks
  {
    var j :| 0 <= j < |s.tasks| && s.tasks[j] == t;
    if Add(s, actor, title, body, id, now).Ok? {
      assert Add(s, actor, title, body, id, now).value.tasks[j] == t;
    }
    if Finish(s, actor, id, reason, k, now).Ok? {
      var i := FindIndex(s.tasks, id).value;
      assert !IsFinished(s.tasks[i].status);
      assert i != j;
      assert Finish(s, actor, id, reason, k, now).value.tasks[j] == t;
    }
  }

  // @assurance:claim task-id-unique
  // @assurance:claim status-matches-last-event
  lemma FinishKeepsInv(s: State, actor: string, id: TaskId, reason: string, k: FinishKind, now: Time)
    requires Inv(s)
    ensures Finish(s, actor, id, reason, k, now).Ok? ==> Inv(Finish(s, actor, id, reason, k, now).value)
  {
    if Finish(s, actor, id, reason, k, now).Ok? {
      var s' := Finish(s, actor, id, reason, k, now).value;
      var n := FindIndex(s.tasks, id).value;
      var e := Event(id, actor, Some(s.tasks[n].status), Target(k), reason, now);
      forall i | 0 <= i < |s'.tasks|
        ensures |EventsOf(s'.events, s'.tasks[i].id)| > 0
        ensures EventsOf(s'.events, s'.tasks[i].id)[|EventsOf(s'.events, s'.tasks[i].id)| - 1].to == s'.tasks[i].status
      {
        EventsOfAppend(s.events, e, s'.tasks[i].id);
        assert s'.tasks[i].id == s.tasks[i].id;
        if i != n {
          assert s.tasks[i].id != id;
        }
      }
    }
  }

  // ---- 一覧の判定の補題 ----

  // @assurance:claim session-required
  // @assurance:claim model-errors-only-when
  lemma ListSessionRequired(actor: string, includeFinished: bool, tasks: seq<Task>)
    ensures ListDecide(actor, includeFinished, tasks) == Err(SessionRequired) <==> IsBlank(actor)
    ensures ListDecide(actor, includeFinished, tasks).Ok? <==> !IsBlank(actor)
  {}

  // 含めないと指定された一覧: 渡されたタスクの集まりのうち、終わっていないタスクを、漏れなく、それだけ、ID が重ならずに（1 度ずつ）。渡された集まりの ID が重ならないこと（主張 db-task-id-unique・db-facts-as-read。DB のモデルでは QueryTasksDistinct が不変条件から導く）を前提にする
  // @assurance:claim task-list-unfinished
  lemma ListUnfinished(actor: string, tasks: seq<Task>)
    requires DistinctIds(tasks) && !IsBlank(actor)
    ensures var r := ListDecide(actor, false, tasks).value;
            (forall t :: t in r <==> t in tasks && !IsFinished(t.status))
            && DistinctIds(r)
  {
    ListContents(tasks, false);
  }

  // 含めると指定された一覧: 渡されたタスクの集まりを、漏れなく、それだけ、ID が重ならずに（1 度ずつ）。渡された集まりの ID が重ならないこと（主張 db-task-id-unique・db-facts-as-read。DB のモデルでは QueryTasksDistinct が不変条件から導く）を前提にする
  // @assurance:claim task-list-all
  lemma ListAll(actor: string, tasks: seq<Task>)
    requires DistinctIds(tasks) && !IsBlank(actor)
    ensures var r := ListDecide(actor, true, tasks).value;
            (forall t :: t in r <==> t in tasks)
            && DistinctIds(r)
  {
    ListContents(tasks, true);
  }

  // 一覧は、作成日時の古い順、同じなら ID の昇順に並ぶ。渡された集まりの ID が重ならないこと（主張 db-task-id-unique・db-facts-as-read）を前提にする
  // @assurance:claim task-list-order
  lemma ListOrder(actor: string, includeFinished: bool, tasks: seq<Task>)
    requires DistinctIds(tasks) && !IsBlank(actor)
    ensures var r := ListDecide(actor, includeFinished, tasks).value;
            forall i, j :: 0 <= i < j < |r| ==>
              r[i].createdAt < r[j].createdAt || (r[i].createdAt == r[j].createdAt && IdLess(r[i].id, r[j].id))
  {
    ListContents(tasks, includeFinished);
  }

  // 状態が不変条件を満たせば、一覧の問い合わせが返す集まりの ID は重ならない（一覧の補題の前提）
  // @assurance:claim task-list-unfinished
  // @assurance:claim task-list-all
  // @assurance:claim task-list-order
  lemma QueryTasksDistinct(s: State)
    requires Inv(s)
    ensures DistinctIds(QueryTasks(s))
  {}

  // ---- 補助の補題 ----

  lemma ListContents(tasks: seq<Task>, includeFinished: bool)
    requires DistinctIds(tasks)
    ensures var r := Sort(Select(tasks, includeFinished));
            (forall t :: t in r <==> t in tasks && (includeFinished || !IsFinished(t.status)))
            && Sorted(r)
            && DistinctIds(r)
  {
    var sel := Select(tasks, includeFinished);
    SelectContents(tasks, includeFinished);
    SelectDistinctIds(tasks, includeFinished);
    SortContents(sel);
  }

  lemma SelectContents(tasks: seq<Task>, includeFinished: bool)
    ensures forall t :: t in Select(tasks, includeFinished) <==> t in tasks && (includeFinished || !IsFinished(t.status))
  {
    if |tasks| > 0 {
      SelectContents(tasks[1..], includeFinished);
      assert tasks == [tasks[0]] + tasks[1..];
    }
  }

  predicate DistinctIds(xs: seq<Task>) {
    forall i, j :: 0 <= i < j < |xs| ==> xs[i].id != xs[j].id
  }

  lemma SelectDistinctIds(tasks: seq<Task>, includeFinished: bool)
    requires DistinctIds(tasks)
    ensures DistinctIds(Select(tasks, includeFinished))
  {
    if |tasks| > 0 {
      SelectDistinctIds(tasks[1..], includeFinished);
      SelectContents(tasks[1..], includeFinished);
      var rest := Select(tasks[1..], includeFinished);
      forall k | 0 <= k < |rest|
        ensures rest[k].id != tasks[0].id
      {
        assert rest[k] in tasks[1..];
      }
    }
  }

  lemma SortContents(xs: seq<Task>)
    requires DistinctIds(xs)
    ensures forall t :: t in Sort(xs) <==> t in xs
    ensures Sorted(Sort(xs))
    ensures |Sort(xs)| == |xs|
    ensures DistinctIds(Sort(xs))
  {
    if |xs| > 0 {
      SortContents(xs[1..]);
      assert xs == [xs[0]] + xs[1..];
      forall t | t in Sort(xs[1..])
        ensures t.id != xs[0].id
      {
        var k :| 0 <= k < |xs[1..]| && xs[1..][k] == t;
        assert xs[k + 1] == t;
      }
      InsertContents(xs[0], Sort(xs[1..]));
    }
  }

  lemma InsertContents(t: Task, xs: seq<Task>)
    requires Sorted(xs) && DistinctIds(xs)
    requires forall x :: x in xs ==> x.id != t.id
    ensures forall x :: x in Insert(t, xs) <==> x == t || x in xs
    ensures Sorted(Insert(t, xs))
    ensures |Insert(t, xs)| == |xs| + 1
    ensures DistinctIds(Insert(t, xs))
  {
    if |xs| > 0 && !Before(t, xs[0]) {
      InsertContents(t, xs[1..]);
      BeforeTotal(t, xs[0]);
      var r := Insert(t, xs[1..]);
      forall j | 0 <= j < |r|
        ensures Before(xs[0], r[j]) && xs[0].id != r[j].id
      {
        assert r[j] == t || r[j] in xs[1..];
        if r[j] != t {
          var k :| 0 <= k < |xs[1..]| && xs[1..][k] == r[j];
          assert xs[k + 1] == r[j];
        }
      }
    } else if |xs| > 0 {
      forall j | 0 <= j < |xs|
        ensures Before(t, xs[j]) && t.id != xs[j].id
      {
        assert xs[j] in xs;
        if j > 0 { BeforeTransitive(t, xs[0], xs[j]); }
      }
    }
  }

  lemma IdLessTransitive(a: string, b: string, c: string)
    requires IdLess(a, b) && IdLess(b, c)
    ensures IdLess(a, c)
  {
    if |a| > 0 && |b| > 0 && |c| > 0 && a[0] == b[0] && b[0] == c[0] {
      IdLessTransitive(a[1..], b[1..], c[1..]);
    }
  }

  lemma IdLessTotal(a: string, b: string)
    requires a != b
    ensures IdLess(a, b) || IdLess(b, a)
  {
    if |a| > 0 && |b| > 0 && a[0] == b[0] {
      assert a == [a[0]] + a[1..] && b == [b[0]] + b[1..];
      IdLessTotal(a[1..], b[1..]);
    }
  }

  lemma BeforeTransitive(a: Task, b: Task, c: Task)
    requires Before(a, b) && Before(b, c)
    ensures Before(a, c)
  {
    if a.createdAt == b.createdAt && b.createdAt == c.createdAt {
      IdLessTransitive(a.id, b.id, c.id);
    }
  }

  lemma BeforeTotal(a: Task, b: Task)
    requires a.id != b.id
    ensures Before(a, b) || Before(b, a)
  {
    IdLessTotal(a.id, b.id);
  }

  lemma EventsOfAppend(events: seq<Event>, e: Event, id: TaskId)
    ensures EventsOf(events + [e], id) == EventsOf(events, id) + (if e.taskId == id then [e] else [])
  {
    assert (events + [e])[..|events + [e]| - 1] == events;
  }

  // 空の状態は不変条件を満たす
  // @assurance:claim task-id-unique
  // @assurance:claim status-matches-last-event
  lemma EmptyInv()
    ensures Inv(Empty)
  {}
}
