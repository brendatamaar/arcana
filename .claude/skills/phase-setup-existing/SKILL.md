---
name: phase-setup-existing
description: Siapkan project yang SUDAH BERJALAN untuk workflow per fase - audit status fase dari git dan repo, gate, lalu bangun tabel status di PLAN.md, detail fase, DECISIONS.md, AGENTS.md dan CLAUDE.md. Pakai saat user minta adopsi/retrofit workflow fase di project lama. Tidak mengubah kode aplikasi.
---

# phase-setup-existing

Tujuan: project lama memakai struktur workflow per fase. JANGAN mengubah kode aplikasi.
Format semua dokumen: `.agents/playbook/formats.md` (baca dulu).

## 1. Audit

Baca `docs/PRD.md`, `docs/PLAN.md`, AGENTS.md dan CLAUDE.md jika ada, `git log --oneline -50`, `git status`, dan struktur repo. Tentukan status tiap fase (done / sebagian / todo) beserta buktinya (commit atau file).

## 2. Gate

Lanjut OTOMATIS ke langkah 3 hanya jika SEMUA syarat terpenuhi:
- status setiap fase didukung bukti yang jelas di repo
- tidak ada fase "sebagian" yang sisa pekerjaannya ambigu
- tidak ada aturan lama di AGENTS.md/CLAUDE.md yang bertentangan dengan workflow ini
- working tree bersih

Jika satu saja tidak terpenuhi: BERHENTI. Laporkan status fase (maksimal 15 baris) dan daftar hal yang perlu diputuskan user, lalu tunggu.

## 3. Bangun struktur

1. PLAN.md: tabel status fase.
2. Detail tiap fase: di PLAN.md, atau di `docs/phases/Pn-<nama>.md` jika lebih dari 5 fase.
   - Fase done: Goal + Handoff notes dari git log saja.
   - Fase sebagian/todo: Tasks (yang selesai dicentang), DoD berupa perintah, Out of scope.
   - DoD yang belum bisa dites: tambahkan task "tulis test X" di awal fase.
   - `Depends on` hanya berisi fase yang benar-benar harus `done`. Jika fase lain hanya butuh bagian yang sudah selesai dari sebuah fase, pecah fase itu (`P3` done + `P3b` sisa). Jangan ada fase yang tercantum di `Depends on` dan `Parallel-safe with` sekaligus.
3. `docs/DECISIONS.md`: 5-10 keputusan penting yang terlihat di kode.
4. AGENTS.md: lengkapi bagian DI LUAR blok `repo-playbook:start/end` dengan stack, perintah test/lint/build, dan konvensi. Pertahankan aturan lama. Jangan ubah isi blok repo-playbook.
5. CLAUDE.md: pastikan ada `@AGENTS.md`. Bagian yang tumpang tindih dipindah ke AGENTS.md; sisakan aturan khusus Claude saja.
6. Commit: `docs: adopt phased workflow`.

## Laporan akhir (maksimal 20 baris)

- status tiap fase
- fase berikutnya yang siap dikerjakan (phase-start) atau dilanjutkan (phase-resume)
- ringkasan perubahan AGENTS.md/CLAUDE.md
- asumsi yang dibuat
