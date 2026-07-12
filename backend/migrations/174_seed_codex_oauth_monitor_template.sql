-- Migration: 174_seed_codex_oauth_monitor_template
-- 内置「Codex OAuth / Luna 兼容」渠道监控模板。
--
-- 背景（#3983）：
--   账户直测会发送 Originator: codex_cli_rs + Codex CLI User-Agent，
--   而渠道监控默认只带 Authorization，上游对 gpt-5.6-luna 等模型会返回
--   404 Model not found，并污染账号 30 分钟冷却。
--
-- 本模板给 responses 检测补上与账户直测一致的 Codex 身份头；
-- 普通 OpenAI-compatible 默认模板保持空 headers 不变。
-- ON CONFLICT DO NOTHING：不覆盖用户已编辑的同名模板。

INSERT INTO channel_monitor_request_templates (
    name, provider, api_mode, description, extra_headers, body_override_mode, body_override
)
VALUES
(
    'Codex OAuth / 本站自检',
    'openai',
    'responses',
    '适用于 Sub2API OpenAI OAuth / Codex 上游自检：POST /v1/responses，附带 Originator 与 Codex CLI User-Agent，避免 Luna 等模型因缺少客户端身份返回 404。',
    '{
        "Originator": "codex_cli_rs",
        "User-Agent": "codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color",
        "Version": "0.144.1"
    }'::jsonb,
    'off',
    NULL
),
(
    'Codex OAuth Chat Completions',
    'openai',
    'chat_completions',
    '适用于走 /v1/chat/completions 的 Codex OAuth 检测：同样附带 Originator 与 Codex CLI User-Agent。',
    '{
        "Originator": "codex_cli_rs",
        "User-Agent": "codex_cli_rs/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color",
        "Version": "0.144.1"
    }'::jsonb,
    'off',
    NULL
)
ON CONFLICT (provider, name) DO NOTHING;
