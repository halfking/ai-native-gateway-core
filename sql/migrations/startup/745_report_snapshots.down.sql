-- 745 down: 移除 report_snapshots 快照表（连带其索引与 UNIQUE 约束）。
-- 本表为设计预埋（消费方 worker 尚未实现），回滚零数据面影响；消费方
-- 落地后回滚前必须先确认无在用的快照行。

DROP TABLE IF EXISTS report_snapshots;
