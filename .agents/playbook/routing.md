# Routing model

File ini milik project: installer hanya membuatnya jika belum ada, dan tidak menimpanya.
Sesuaikan setelah membandingkan model pada fase nyata.

| Peran | Utama | Fallback 1 | Fallback 2 |
|---|---|---|---|
| Planning, spec, review PRD/PLAN | claudeAgent claude-opus-5-5 (effort high) | codex gpt-6.2-sol (reasoningEffort high) | antigravity gemini-4-neon (thinking_level high) |
| Eksekusi fase rutin | claudeAgent claude-sonnet-5-5 (effort medium) | codex gpt-6.2-sol (reasoningEffort medium) | antigravity gemini-4-neon (thinking_level medium) |
| Eksekusi fase inti/sulit | claudeAgent claude-opus-5-5 (effort high) | codex gpt-6.2-sol (reasoningEffort high) | antigravity gemini-4-neon (thinking_level high) |
| Review silang | keluarga model BERBEDA dari penulis kode: codex gpt-6.2-sol (medium) | antigravity gemini-4-neon (medium) | claudeAgent claude-opus-5-5 (medium) |
| Eksplorasi / cari kode | claudeAgent claude-haiku-5-5 (effort low) | antigravity gemini-3.8-flash (thinking_level low) | - |

Aturan:
- Review silang: lewati baris yang keluarganya sama dengan model penulis kode.
- Opsi (`effort`, `reasoningEffort`, `thinking_level`) mengikuti nama opsi dari `orchestrator_capabilities`.
