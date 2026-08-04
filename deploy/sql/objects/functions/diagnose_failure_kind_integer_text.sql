--
-- Name: diagnose_failure_kind(integer, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.diagnose_failure_kind(p_status integer, p_body text) RETURNS text
    LANGUAGE plpgsql IMMUTABLE
    AS $_$
DECLARE
    v_body text := COALESCE(p_body, '');
    v_has_tool boolean;
    v_has_ctx_window boolean;
    v_has_minimax_auth boolean;
    v_has_minimax_quota boolean;
    v_status int := COALESCE(p_status, 0);
BEGIN
    -- Pre-compute MiniMax vendor-private signals once.
    -- These mirror the Go-side regex set in errorsx/classify.go (P2):
    --   - "tool id" / "tool call id" / "tool result's tool id" with
    --     "not found" / "invalid" / "2013" suffix
    --   - "context window exceeds limit" / "context window exceeded" /
    --     "maximum context length" / "context_length_exceeded"
    --   - "authorized_error" / "login fail" / "1004" / "1005"
    --   - "balance insufficient" / "1008" / "quota" / "余额"
    v_has_tool := v_body ~* '(tool[_ ]?(call[_ ]?id|use[_ ]?id|result.*tool[_ ]?id).{0,100}(not found|not exist|invalid|unknown|does not exist)|tool[^a-z].{0,80}2013)';
    v_has_ctx_window := v_body ~* '(context[ _-]?length[ _-]?exceeded|maximum context length|context[ _-]?window[ _-]?(exceeded|exceeds limit|is)|prompt is too long|input is too long|too many (input )?tokens|tokens? exceed|reduce the length|maximum number of tokens|上下文(长度)?(超出|超过|超限)|超出(模型)?(最大)?(上下文|长度|限制))';
    v_has_minimax_auth := v_body ~* '(authorized[_-]?error|login fail|api[ _-]?key.{0,30}(invalid|expired|revoked)|(?:^|[^0-9])(1004|1005)(?:\)|[^0-9]|$))';
    v_has_minimax_quota := v_body ~* '(余额不足|balance.{0,20}insufficient|quota.{0,20}(exhaust|exceed|insufficient)|账户.{0,10}(欠费|余额)|insufficient (credit|balance|quota)|(balance|账户余额).{0,30}(不足|不够|欠费|1008))';

    -- Status-code first when there is no body to peek (the 5xx and
    -- status-only paths the Go side handles in ClassifyResponseStatus).
    IF v_body = '' THEN
        IF v_status = 401 OR v_status = 403 THEN
            -- MiniMax 1004/1005 codes only surface in the body; a
            -- status-only 401/403 still maps to auth.
            RETURN 'auth';
        ELSIF v_status = 402 THEN
            RETURN 'quota';
        ELSIF v_status = 429 THEN
            RETURN 'rate_limit';
        ELSIF v_status IN (503, 529) THEN
            RETURN 'concurrent';
        ELSIF v_status >= 500 THEN
            RETURN 'upstream_down';
        ELSIF v_status = 408 THEN
            RETURN 'timeout';
        ELSIF v_status IN (405, 406, 409, 410, 411, 412, 415, 416, 417, 418, 421, 423, 424, 425, 426, 428, 431) THEN
            RETURN 'unsupported_feature';
        ELSIF v_status = 413 THEN
            RETURN 'context_length_exceeded';
        ELSE
            RETURN 'transient';
        END IF;
    END IF;

    -- Body-peek path mirrors Go ClassifyErrorWithBody.
    -- Order matters: concurrent → auth → quota → model_not_found →
    -- unsupported_feature → tool_call_id → context_length.
    IF v_body ~* '(concurrent.{0,30}(limit|exceed|over|too many|reach|max)|too many (concurrent|requests|connections)|(engine|server|service|api) (overloaded|too busy|busy)|(server|service|upstream) (is )?(overload|under pressure)|(rpm|tpm).{0,20}(limit|exceed|reach|over)|request(ed|s)? too (fast|frequent|many)|slow down|try again later|backoff|并发.{0,15}(超限|过大|过高|达到上限|超过限制)|请求.{0,10}(过快|频繁|太多)|服务.{0,10}(繁忙|过载|压力|降级)|稍后重试|限流)' THEN
        RETURN 'concurrent';
    END IF;

    -- 401 with a non-empty body that hints at credential failure
    -- (api key / token / unauthorized) is auth regardless of vendor
    -- format. The Go-side ClassifyResponseStatus maps 401 → KindAuth
    -- but only when the body is empty; here we extend the same
    -- intent to bodies that contain auth-shaped strings.
    -- 2026-06-30 (P3): added during migration 057 backfill validation.
    -- 401 with a non-empty body that hints at credential failure
    -- is auth regardless of vendor format. The Go-side
    -- ClassifyResponseStatus maps 401 → KindAuth but only when the
    -- body is empty; here we extend the same intent to bodies
    -- that contain auth-shaped strings.
    --
    -- Two flavours of pattern:
    --  (a) "api key" / "token" / "unauthorized" / etc. with a
    --      qualifying bad-state word (expired / invalid / revoked
    --      / failed / required / missing) — catches most vendor
    --      auth error formats.
    --  (b) vendor-private strings with the auth meaning but no
    --      qualifying word, e.g. MiniMax's "login fail" / "please
    --      carry the api secret" (1004) — caught by minimaxAuthRe
    --      below but duplicated here for clarity.
    --  (c) "invalid ... (api key|token|credential|key)" — handles
    --      the common "invalid api key" / "invalid token" shape
    --      where the qualifier (invalid) and the noun are not
    --      adjacent.
    -- 2026-06-30 (P3): added during migration 057 backfill.
    IF v_status = 401 AND v_body ~* '((api[ _-]?key|token|credential|secret|unauthor|forbidden|access.denied|subscription|plan|billing|payment).{0,40}(expired|invalid|revoked|terminated|failed|required|missing|expire|disable))|(invalid|wrong|bad).{0,30}(api[ _-]?key|token|credential|secret|key)|login.fail|please.carry|the.api.secret' THEN
        RETURN 'auth';
    END IF;

    IF v_has_minimax_auth THEN
        RETURN 'auth';
    END IF;

    IF v_status <> 429 AND v_has_minimax_quota THEN
        RETURN 'quota';
    END IF;

    IF v_status IN (400, 404, 422) AND v_body ~* '((^|[^a-z0-9])(model|endpoint)[\s:]+["'']?[a-z0-9._\-/:]{1,80}["'']?\s+(does not exist|is not found|not found|is unknown|unknown)([^a-z0-9]|$)|(^|[^a-z0-9])(no such|unknown)\s+model([^a-z0-9]|$)|模型不存在|模型.{0,10}(不存在|未找到))' THEN
        RETURN 'model_not_found';
    END IF;

    IF v_body ~* '((does not|doesn''?t) support (coding plan|tool|function|tools|function call)|(tool|function)[- _]?call(ing|s)? (is )?not supported|unsupported (parameter|model|feature).{0,20}(tools?|function|tool_choice)|当前模型不支持)' THEN
        RETURN 'unsupported_feature';
    END IF;

    -- 2026-06-30 P2 fix: contextLength check runs BEFORE tool_call_id
    -- because MiniMax's vendor-private (2013) code is shared across
    -- both error categories. Without this, the "context window
    -- exceeds limit (2013)" body would match the tool[^a-z]…2013
    -- fallback in toolCallIdMismatchRe and be mis-classified.
    IF v_status IN (400, 413, 422) AND v_has_ctx_window THEN
        RETURN 'context_length_exceeded';
    END IF;

    IF v_has_tool THEN
        RETURN 'tool_call_id_mismatch';
    END IF;

    -- 429 status with a MiniMax-style body that does NOT trigger the
    -- concurrent overload regex falls through to rate_limit (the
    -- default ClassifyResponseStatus mapping).
    IF v_status = 429 THEN
        RETURN 'rate_limit';
    END IF;

    IF v_status >= 500 THEN
        RETURN 'upstream_down';
    END IF;

    -- Generic "not found" / "page not found" body on 404 — the
    -- Go-side ClassifyErrorWithBody doesn't have a regex for this
    -- generic shape, so it falls through to status-only which
    -- returns KindTransient. That's wrong: a 404 from the upstream
    -- means the requested resource (model / endpoint / function)
    -- doesn't exist, which is non-retryable. Classify as
    -- unsupported_feature so the circuit isn't cooled and the
    -- cross-credential retry fast-path is skipped (no other
    -- credential will return a different 404 for the same client_model).
    -- 2026-06-30 (P3): added during migration 057 backfill
    -- validation when 2540 'upstream 404: 404 page not found' rows
    -- surfaced as KindTransient instead of unsupported_feature.
    IF v_status IN (400, 404, 422) AND NOT v_has_tool AND NOT v_has_ctx_window THEN
        RETURN 'unsupported_feature';
    END IF;

    -- 403 with a "subscription expired" / "plan limit" body
    -- should be KindAuth or KindQuota, not transient. The Go-side
    -- ClassifyResponseStatus maps 401/403 → KindAuth, but with a
    -- status-only body the function also returns auth via the
    -- v_body = '' branch. When a body IS present and matches
    -- subscription-style strings, upgrade to auth (it IS a
    -- credential-level failure — operator wants the credential
    -- pulled from rotation).
    -- 2026-06-30 (P3): added during migration 057 backfill
    -- validation when 'Coding Plan subscription is expired' rows
    -- surfaced as KindTransient.
    IF v_status = 403 AND v_body ~* '(subscription|plan|quota|billing|payment|expired|cancelled|canceled|terminated).*(expired|invalid|revoked|terminated|failed)|access.denied|forbidden|payment.required' THEN
        RETURN 'auth';
    END IF;

    RETURN 'transient';
END;
$_$;

