-- 889_session_turns_tools_population.down.sql —— 回滚 889 的注释订正
--
-- 回滚语义 = **回到迁移前的库状态**（不是「回到正确状态」）：
--   · session_turns.tools / session_turns_hot.tools 的注释还原成 456 的原文
--     'Tools invoked during this turn (JSON array of tool_use records).'；
--   · session_tools.turn_no / session_tools_hot.turn_no 的注释**删除**
--     （706 建列时本来就没有列注释 ⇒ 还原 = 无注释）。
--
-- ⚠ **不撤销写路径的填充**：889 只改注释，填充在
--   domains/session/v2/turn_writer.go 的 turnToolsJSONBExpr（代码侧），
--   回滚本文件不会让已写入的 tools 值消失。要停掉填充请改代码。
--
-- ⚠ **不动表结构、不删列、不删数据**：tools 列保留（删列会撞上
--   710/816/817/824 的视图投影 + db/request_logs_view_schema.go 的
--   sessionFamilyProjection + 三份基线快照，见 889 头部）。

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

DO $comment_889_down$
BEGIN
  IF to_regclass('public.session_turns') IS NOT NULL THEN
    COMMENT ON COLUMN public.session_turns.tools IS
      'Tools invoked during this turn (JSON array of tool_use records).';
  END IF;

  IF to_regclass('public.session_turns_hot') IS NOT NULL THEN
    COMMENT ON COLUMN public.session_turns_hot.tools IS
      'Tools invoked during this turn (JSON array of tool_use records).';
  END IF;

  IF to_regclass('public.session_tools') IS NOT NULL THEN
    COMMENT ON COLUMN public.session_tools.turn_no IS NULL;
  END IF;

  IF to_regclass('public.session_tools_hot') IS NOT NULL THEN
    COMMENT ON COLUMN public.session_tools_hot.turn_no IS NULL;
  END IF;
END
$comment_889_down$;

COMMIT;
