package ops

// 実行時の確認の関数を、テストから直接呼ぶための入り口
var (
	CheckAddWriteForTest    = checkAddWrite
	CheckFinishWriteForTest = checkFinishWrite
)
