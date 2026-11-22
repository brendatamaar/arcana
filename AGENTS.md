# Agent rules

<!-- Stack, perintah test/lint/build, dan konvensi: diisi oleh skill phase-setup-new / phase-setup-existing. -->

<!-- repo-playbook:start (dikelola oleh repo-playbook/install.sh; jangan edit manual) -->
## Workflow per fase

- Sumber kebenaran: `docs/PLAN.md` (status fase), detail fase di PLAN.md atau `docs/phases/`, keputusan di `docs/DECISIONS.md`. Format: `.agents/playbook/formats.md`.
- Skill tersedia: phase-setup-new, phase-setup-existing, phase-start, phase-resume, phase-status, phase-routing. Jika skill tidak termuat otomatis, baca `.agents/skills/<nama>/SKILL.md` lalu ikuti isinya.
- Sebelum kerja: baca PLAN.md + detail fase yang ditugaskan. Kerjakan HANYA scope fase; hal di luar scope dicatat di Handoff notes.
- Selesai = DoD fase lulus, dijalankan sendiri. DoD gagal 3x berturut-turut: berhenti dan laporkan.
- Commit kecil. Update Handoff notes di tengah & akhir fase, dan status di PLAN.md.
- Keputusan teknis baru: tambah 1 entri di DECISIONS.md.
- Delegasi ke provider lain dan fallback saat limit: ikuti skill phase-routing (`.agents/playbook/routing.md`).
- Laporan ringkas; jangan dump file atau log panjang.
<!-- repo-playbook:end -->
