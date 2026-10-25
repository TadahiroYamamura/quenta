// タスクの状態と操作（#1）
//
// 実装（Go）が呼ぶ関数は 2 種類ある。
// - 1 件のタスクについての判定と遷移: NewTask、FinishArgsError、Finish、IsVisible、ValidTask、IsBlank
//   実装の登録・完了・中止・一覧は、これらを通る。これらの関数の戻り値について証明した性質は、実装で「証明」で守られる。
//   戻り値を受け取った後のこと（保存する、全件を読む、など）は Go と SQL が行うので、守り方は性質ごとのコメントに書く
// - 全体の状態（State）についての関数: Empty、Add、FinishTask、Show、List、Valid
//   実装は同じことをデータベースで行い、これらの関数を通らない。これらは手本で、テストで実装と突き合わせるためだけに呼ぶ。
//   これらについて証明した性質は、実装では「証明」で守られない。守り方は、性質ごとのコメントに書く
//
// 性質ごとのコメントの「重要度」「守るもの」「根拠」は、CLAUDE.md の「保証の対応表」に従う。
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
  // 全角の空白（U+3000）を含み、ゼロ幅スペース（U+200B）は含まない。
  // 重要度: 中（受け入れ条件 3・9 の「空」の意味）。守るもの: 証明（IsBlank を通じて NewTask・FinishArgsError が使う）とテスト。
  // 根拠: この定義が Go の unicode.IsSpace と一致することは、TestIsBlankはUnicodeの全文字でGoのunicode_IsSpaceと一致する で確かめる
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

  // 一覧に出すか。includeFinished が false なら終わっていないタスクだけ、true ならすべて（受け入れ条件 4・5）
  // 重要度: 中。守るもの: 証明（実装は IsVisible で判定する。補題はなく、性質はこの定義そのもの。定義が意図どおりかは人がレビューで確かめる）。
  // 根拠: 実装の一覧は、DB から読んだタスクを 1 件ずつこの述語に通して絞る
  predicate IsVisible(t: Task, includeFinished: bool)
  {
    includeFinished || !IsFinished(t.status)
  }

  // 1 件のタスクの不変条件: タイトルが空でない
  // 重要度: 中。守るもの: 証明（NewTask・Finish。補題 NewTaskResult・FinishOneResult）と、DB から読んだときの確認（実装が ValidTask を呼ぶ）。
  // 根拠: 実装が保存するタスクは、NewTask か Finish が返したものだけ。DB から読んだタスクは、Finish・IsVisible に渡す前に、
  // 実装がこの述語で確かめ、満たさなければ internal のエラーにする（テスト: Test保存されたタスクが不変条件を満たさなければ完了も一覧もinternalを返す）
  predicate ValidTask(t: Task)
  {
    !IsBlank(t.title)
  }

  // ---------- 1 件のタスクについての性質（実装が呼ぶ関数についての証明） ----------

  // 登録の結果: 主体とタイトルが空でなければ、todo のタスクと登録のイベントを返す。
  // 主体が空なら SessionRequired、主体があってタイトルが空なら TitleRequired。
  // 返すタスクは ValidTask を満たし、イベントは形が正しく、そのイベントだけでタスクの経緯が正しい
  // 重要度: 中（受け入れ条件 1〜3）。守るもの: 証明（NewTask）。根拠: 実装の登録は、必ず NewTask の結果を保存する
  lemma NewTaskResult(id: string, title: string, body: string, actor: string, now: UnixMillis)
    ensures IsBlank(actor) ==> NewTask(id, title, body, actor, now) == Err(SessionRequired)
    ensures !IsBlank(actor) && IsBlank(title) ==> NewTask(id, title, body, actor, now) == Err(TitleRequired)
    ensures !IsBlank(actor) && !IsBlank(title) ==>
      var r := NewTask(id, title, body, actor, now);
      r.Ok?
      && r.value.0 == Task(id, title, body, Todo, now)
      && r.value.1 == Event(id, actor, None, Todo, None, now)
      && ValidTask(r.value.0)
      && WellFormedEvent(r.value.1)
      && HistoryMatches(r.value.0, [r.value.1])
  {
  }

  // 完了・中止の引数の判定の結果: 主体が空なら SessionRequired、主体があって理由が空なら ReasonRequired、どちらもあれば None
  // 重要度: 中（受け入れ条件 9）。守るもの: 証明（FinishArgsError）。根拠: 実装の完了・中止は、タスクを探す前に必ず FinishArgsError を呼ぶ
  lemma FinishArgsErrorResult(reason: string, actor: string)
    ensures IsBlank(actor) ==> FinishArgsError(reason, actor) == Some(SessionRequired)
    ensures !IsBlank(actor) && IsBlank(reason) ==> FinishArgsError(reason, actor) == Some(ReasonRequired)
    ensures !IsBlank(actor) && !IsBlank(reason) ==> FinishArgsError(reason, actor) == None
  {
  }

  // 終わったタスクには、Finish は必ずエラーを返す（終わったタスクの状態は変えられない）
  // 重要度: 高（受け入れ条件 10）。守るもの: 証明（Finish）と DB の制約（終わったタスクの書き換えを拒むトリガー（tasks_finished_frozen））。
  // 根拠: 実装がタスクを書き換えるのは完了・中止だけで、DB から読んだタスクを必ず Finish に通し、Finish が Ok を返したときだけ保存する。
  // この前提がコードの変更で崩れても、トリガーが拒む（テスト: Test終わったタスクは書き換えられない）。
  // エラーのときにタスクが変わらないこと（受け入れ条件 10 の後半）は、保存しないことで守る（テスト: Test変更がエラーを返すとタスクもイベントも保存しない）
  lemma FinishRejectsFinished(t: Task, o: Outcome, reason: string, actor: string, now: UnixMillis)
    requires IsFinished(t.status)
    ensures Finish(t, o, reason, actor, now).Err?
  {
  }

  // 完了・中止の結果。
  // - 引数の誤りがあればそのエラー、なければ、終わったタスクには AlreadyFinished、終わっていないタスクには Ok を返す
  // - Ok なら、タスクは状態だけが終わらせ方の状態に変わり（ID・タイトル・本文・作成日時は変わらない）、
  //   イベントは、変更前がもとの状態、変更後が新しい状態、理由と主体が引数のもので、形が正しい
  // - もとのタスクが ValidTask を満たせば、新しいタスクも満たす
  // - もとのタスクの経緯（イベントの列）が正しければ、そこに返したイベントを足した列で、新しいタスクの経緯も正しい
  // 重要度: 高（受け入れ条件 7・8、経緯を残す）。守るもの: 証明（Finish）と DB の制約（イベントは追記だけ、EventsAppendOnly を参照）。
  // 根拠: 実装の完了・中止は、Finish が返したタスクとイベントを 1 つのトランザクションで保存する
  //（テスト: Test変更がエラーを返すとタスクもイベントも保存しない、Testイベントを保存できなければタスクの変更も残らない）。
  // 「経緯が正しいまま」は、DB から読んだタスクとイベントで経緯が正しいことを前提にする。その前提の守り方:
  // - ID・作成日時を書き換えない、イベントを消さず書き換えない: DB の制約（tasks の状態以外の列の書き換えを拒むトリガー（tasks_only_status_changes）、
  //   events の書き換え・削除を拒むトリガー（events_no_update・events_no_delete））
  // - タスクの状態の書き換えには、必ず対応するイベントが伴う: Go の作りとテスト。
  //   Go の作り: 保存の層が受け取るのは、Finish・NewTask が返したタスクとイベントの組（状態の変更）だけで、その値は、変換用のパッケージの外からは
  //   仕様の関数の結果からしか作れない。
  //   tasks と events に書き込むのは保存の層の 1 つの関数だけで、組を 1 つのトランザクションで続けて書く。
  //   テスト: Test無作為な操作の列で…（毎歩、実装の状態に Valid を当てる）、Testイベントを保存できなければタスクの変更も残らない、
  //   Test登録のイベントを保存できなければタスクも残らない、Test仕様の関数が作っていない状態の変更や別のタスクの変更は保存しない。
  //   変異テスト: 完了・中止でイベントを保存しないよう壊すと、AC07・AC08・AC12 ほかが落ちることを確かめた。
  //   重要度 高をテストで守る部分（2026-10-25 の仕様のレビューで、人が了承した）
  lemma FinishOneResult(t: Task, o: Outcome, reason: string, actor: string, now: UnixMillis)
    ensures FinishArgsError(reason, actor).Some? ==>
      Finish(t, o, reason, actor, now) == Err(FinishArgsError(reason, actor).value)
    ensures FinishArgsError(reason, actor).None? && IsFinished(t.status) ==>
      Finish(t, o, reason, actor, now) == Err(AlreadyFinished)
    ensures FinishArgsError(reason, actor).None? && !IsFinished(t.status) ==>
      var r := Finish(t, o, reason, actor, now);
      r.Ok?
      && r.value.0 == t.(status := OutcomeStatus(o))
      && r.value.1 == Event(t.id, actor, Some(t.status), OutcomeStatus(o), Some(reason), now)
      && WellFormedEvent(r.value.1)
      && (ValidTask(t) ==> ValidTask(r.value.0))
      && (forall evs :: HistoryMatches(t, evs) ==> HistoryMatches(r.value.0, evs + [r.value.1]))
  {
  }

  // ---------- 全体の状態（手本。実装はこれらの関数を通らない） ----------

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
  // 重要度: 高。守るもの: DB の制約（tasks.id の一意の制約）とテスト。
  // 根拠: 実装は Add を通らない。ID が重なる保存は一意の制約で拒まれ、実装は ID を作り直す。
  // テスト: Test既にあるIDで保存しようとするとid_conflictを返し既存のタスクもイベントも変わらない（制約）、
  // Test作ったIDが既にあれば作り直して登録し既存のタスクは変わらない（ID の作り方を差し替えてわざと重ねる）、
  // TestIDが既にあるものしか作れなければinternalを返し何も登録しない、
  // Test無作為な操作の列で実装は仕様の手本と同じ結果になり仕様の性質が成り立つ（少ない候補の ID で重ねる）。
  // 変異テスト: 上書きで保存する、作り直さない、と壊すと落ちることを確かめた
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

  // 全体の不変条件。条件ごとの守り方:
  // - UniqueIds: 上の UniqueIds のコメントを参照
  // - タイトルが空でない: ValidTask のコメントを参照
  // - イベントの形が正しい（重要度: 高）: 証明（NewTask・Finish。補題 NewTaskResult・FinishOneResult）。実装が保存するイベントは NewTask か Finish が返したものだけ
  // - イベントの対象のタスクがある（重要度: 高）: DB の制約（events.task_id の外部キー）。
  //   テスト: Test存在しないタスクを指すイベントは外部キーの制約で保存できない
  // - タスクの経緯が正しい（重要度: 高）: 証明（NewTask・Finish。補題 NewTaskResult は登録のイベントだけで経緯が正しいこと、
  //   FinishOneResult は正しい経緯にイベントを足しても正しいこと）と、DB の制約（イベントは追記だけ、EventsAppendOnly を参照）。
  //   状態の書き換えに必ずイベントが伴うことは、Go の作りとテストで守る（FinishOneResult のコメントを参照。人が了承した）
  // 実装の状態でこの不変条件が成り立つことは、テスト（Test無作為な操作の列で…）でも、実装から読み出した状態に Valid を当てて確かめる。
  // この判定が見分けられることは、TestValidStateは不変条件を破る状態をfalseにする で確かめる
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

  // 登録は不変条件を保つ（手本。実装での守り方は Valid のコメントを参照）
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

  // 完了・中止は不変条件を保つ（手本。実装での守り方は Valid のコメントを参照）
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

  // 登録の結果（手本）: 登録したタスクとイベントが末尾に 1 件ずつ増え、既存のものは変わらない
  // 重要度: 高（既存のタスクが書き換わるとデータが壊れる）。
  // 守るもの: 証明（NewTask。補題 NewTaskResult。足すタスクとイベントの中身）、DB の制約（tasks.id の一意の制約は同じ ID の 2 件目を拒む。
  // 上書き（INSERT OR REPLACE）による削除は、tasks の削除を拒むトリガー（tasks_no_delete。recursive_triggers を有効にして、上書きによる削除でも起動させる）が拒む。
  // UPDATE による上書きは、tasks の状態以外の列の書き換えを拒むトリガー（tasks_only_status_changes）が拒む）、テスト（Test上書きで保存しようとすると拒まれる）。
  // todo のタスクの状態の書き換えは、トリガーでは拒めない（終わったタスクは FinishRejectsFinished のトリガーが拒む）。FinishOneResult のコメントの「状態の書き換えには、必ず対応するイベントが伴う」の
  // 守り方（Go の作りとテスト。人が了承した）による
  // 根拠: 実装は Add を通らず、INSERT で 1 行足すだけ。テスト: Test既にあるIDで保存しようとするとid_conflictを返し既存のタスクもイベントも変わらない、
  // Test無作為な操作の列で…（毎歩、実装の全タスクを手本と比べる）
  lemma AddResult(st: State, id: string, title: string, body: string, actor: string, now: UnixMillis)
    requires Add(st, id, title, body, actor, now).Ok?
    ensures var st' := Add(st, id, title, body, actor, now).value;
      st'.tasks == st.tasks + [Task(id, title, body, Todo, now)]
      && st'.events == st.events + [Event(id, actor, None, Todo, None, now)]
  {
  }

  // 登録のエラーの条件（手本）。主体 → タイトル → ID の重なりの順に判定する
  // 重要度: 中。守るもの: 証明（NewTask。補題 NewTaskResult。主体とタイトル）と、ID の重なりは UniqueIds のコメントを参照
  lemma AddErrors(st: State, id: string, title: string, body: string, actor: string, now: UnixMillis)
    ensures IsBlank(actor) ==> Add(st, id, title, body, actor, now) == Err(SessionRequired)
    ensures !IsBlank(actor) && IsBlank(title) ==> Add(st, id, title, body, actor, now) == Err(TitleRequired)
    ensures !IsBlank(actor) && !IsBlank(title) && HasTask(st, id) ==> Add(st, id, title, body, actor, now) == Err(IdConflict)
    ensures !IsBlank(actor) && !IsBlank(title) && !HasTask(st, id) ==> Add(st, id, title, body, actor, now).Ok?
  {
  }

  // 完了・中止の結果（手本）: 対象のタスクだけが変わり、イベントが末尾に 1 件増える
  // 重要度: 高（ほかのタスクが書き換わるとデータが壊れる）。
  // 守るもの: 証明（Finish。補題 FinishOneResult。対象の変わり方とイベントの中身）、DB の制約（tasks.id の一意の制約）、テスト（ほかのタスクが変わらないこと）。
  // 根拠: 実装は FinishTask を通らず、ID を条件に 1 行だけ書き換える。ID は一意の制約で重ならないので、条件に合うのは 1 行だけ。
  // 実装は、書き換えた行がちょうど 1 行であることを実行時に確かめ、違えばエラーにしてトランザクションを取り消す。
  // 「ID を条件にする」ことは Go と SQL にあり、テストで守る（2026-10-25 の仕様のレビューで、人が了承した）。条件が外れても、終わったタスクへの書き換えは
  // 終わったタスクの書き換えを拒むトリガー（tasks_finished_frozen）が拒む。
  // テスト: Test無作為な操作の列で…（毎歩、実装の全タスクを手本と比べる）。変異テスト: 書き換えの条件を外すと AC04・05・06・10 ほかが落ちることを確かめた
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

  // 完了・中止のエラーの条件（手本）。主体 → 理由 → ID の有無 → 終わっているか、の順に判定する
  // 重要度: 中（受け入れ条件 9〜11）。守るもの: 証明（FinishArgsError・Finish。補題 FinishArgsErrorResult・FinishOneResult）とテスト。
  // 根拠: 引数の判定と、終わっているかの判定は、実装が呼ぶ関数にある。引数の判定をタスクを探す前に行う順と、
  // ID がなければ NotFound を返すことは、Go と SQL で行うのでテストで守る。
  // テスト: Test完了と中止は引数の誤りをタスクを探す前に返す、Test存在しないIDを読むとnot_foundを返す、Test無作為な操作の列で…。
  // 変異テスト: 引数の判定を先にしない、ID がないときに not_found 以外を返す、と壊すとそれぞれ落ちることを確かめた
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

  // 終わったタスクは、どの操作でも変わらない（手本）
  // 実装での守り方は、完了・中止は FinishRejectsFinished、登録は AddResult のコメントを参照
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

  // タスクは消えない（手本）: 登録でも完了・中止でも、既存のタスクは同じ位置（登録した順）に同じ ID で残る
  // 重要度: 高。守るもの: DB の制約（tasks の削除を拒むトリガー（tasks_no_delete。recursive_triggers を有効にして、上書きによる削除でも起動させる）、
  // tasks の状態以外の列の書き換えを拒むトリガー（tasks_only_status_changes））とテスト。
  // 根拠: 実装は Add・FinishTask を通らない。テスト: Testタスクは削除できない、Test上書きで保存しようとすると拒まれる、
  // Testタスクの状態以外の列は書き換えられない、
  // Test無作為な操作の列で…（毎歩、実装の全タスクを手本と比べる）
  lemma TasksNeverDisappear(st: State, id: string, title: string, body: string, o: Outcome, reason: string, actor: string, now: UnixMillis)
    ensures Add(st, id, title, body, actor, now).Ok? ==>
      var ts := Add(st, id, title, body, actor, now).value.tasks;
      |ts| == |st.tasks| + 1 && forall k :: 0 <= k < |st.tasks| ==> ts[k].id == st.tasks[k].id
    ensures FinishTask(st, id, o, reason, actor, now).Ok? ==>
      var ts := FinishTask(st, id, o, reason, actor, now).value.tasks;
      |ts| == |st.tasks| && forall k :: 0 <= k < |st.tasks| ==> ts[k].id == st.tasks[k].id
  {
  }

  // イベントは追記だけ（手本）: 登録でも完了・中止でも、既存のイベントは消えず書き換わらず、末尾に 1 件増える
  // 重要度: 高（経緯を残す）。守り方は部分ごとに分かれる。
  // - 既存のイベントが消えず書き換わらない: DB の制約（events の書き換えと削除を拒むトリガー（events_no_update・events_no_delete））。
  //   テスト: Testイベントは書き換えも削除もできない
  // - 末尾に足される: DB の制約（events.seq は AUTOINCREMENT で、記録した順の番号は増える一方）
  // - 1 回の操作で 1 件だけ足される: Go の作り（tasks と events に書き込むのは保存の層の 1 つの関数だけで、イベントを 1 件だけ書く）とテスト。
  //   テスト: Test無作為な操作の列で…（毎歩、イベントの数と中身を手本と比べ、それまでのイベントが先頭にそのまま残ることを確かめる）。
  //   変異テスト: 完了・中止でイベントを 2 件書くよう壊すと、AC07・AC08・Test無作為な操作の列で… ほかが落ちることを確かめた。
  //   重要度 高をテストで守る部分（2026-10-25、「状態の書き換えに必ずイベントが伴う」の了承に含めることを人が了承した）
  // 根拠: 実装は Add・FinishTask を通らない
  lemma EventsAppendOnly(st: State, id: string, title: string, body: string, o: Outcome, reason: string, actor: string, now: UnixMillis)
    ensures Add(st, id, title, body, actor, now).Ok? ==>
      var ev := Add(st, id, title, body, actor, now).value.events;
      |ev| == |st.events| + 1 && ev[..|st.events|] == st.events
    ensures FinishTask(st, id, o, reason, actor, now).Ok? ==>
      var ev := FinishTask(st, id, o, reason, actor, now).value.events;
      |ev| == |st.events| + 1 && ev[..|st.events|] == st.events
  {
  }

  // 一覧（手本）: ID が重ならない状態で、一覧に出すタスクがちょうど 1 回ずつ、作成日時の古い順（同じなら登録した順）に並ぶ
  // 守り方は部分ごとに分かれる。
  // - どのタスクを出すかの 1 件ごとの判定（重要度: 中）: IsVisible の定義（実装はこの述語で判定する）
  // - 全件を 1 回ずつ出すこと（重要度: 中。受け入れ条件 4・5）: テスト。実装は全件を DB から読み、1 件ずつ判定する。
  //   テスト: Test無作為な操作の列で…（一覧を手本と比べる）、AC04・AC05。変異テスト: 1 件飛ばすと AC05 ほかが落ちることを確かめた
  // - 並び順（重要度: 低）: テスト。実装は SQL で並べる。テスト: Test一覧は作成日時の古い順で作成日時が同じなら登録した順に返す、Test無作為な操作の列で…
  // 全体を一度に証明させると、証明器の乱数の種によって数秒から 2 分以上までばらつき、CI で時間切れになった（#16）。
  // 主張ごとに分けると、どの種でも 1 件 0.3 秒以内で終わる
  lemma {:isolate_assertions} ListOrdered(st: State, includeFinished: bool)
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
