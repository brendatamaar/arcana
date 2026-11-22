---
name: phase-setup-new
description: Siapkan project BARU (docs/PRD.md dan docs/PLAN.md sudah ada, belum ada implementasi) untuk workflow per fase - review kritis PRD/PLAN, gate, lalu bangun struktur fase, DECISIONS.md, AGENTS.md dan CLAUDE.md. Pakai saat user minta setup/persiapan project baru dari PRD dan PLAN. Tidak menulis kode aplikasi.
---

# phase-setup-new

Tujuan: project baru siap dieksekusi per fase. JANGAN menulis kode aplikasi.
Format semua dokumen: `.agents/playbook/formats.md` (baca dulu).

## 1. Review

Baca `docs/PRD.md`, `docs/PLAN.md`, serta AGENTS.md dan CLAUDE.md jika ada. Cari:

- acceptance criteria PRD yang tidak tercakup fase mana pun
- fase terlalu besar (lebih dari 1 sesi kerja atau diff yang sulit direview)
- dependensi antar-fase yang salah atau hilang
- DoD yang tidak bisa dites dengan perintah
- risiko teknis yang terlewat
- komponen ber-state kritis yang layak dibuat FSM + spec formal (sebutkan saja, jangan dibuat)

Klasifikasikan setiap temuan:
- SERIUS: mengubah scope, arsitektur, atau urutan fase
- MINOR: bisa diperbaiki tanpa mengubah maksud PRD/PLAN

## 2. Gate

Lanjut OTOMATIS ke langkah 3 hanya jika:
- tidak ada temuan SERIUS, dan
- tidak ada aturan lama di AGENTS.md/CLAUDE.md yang bertentangan dengan workflow ini.

Jika tidak: BERHENTI. Tampilkan temuan (maksimal 15, urut dari paling serius) dan tunggu keputusan user.

## 3. Bangun struktur

1. Perbaiki temuan MINOR di PLAN.md.
2. PLAN.md: arsitektur ringkas + tabel fase (semua Status=todo).
3. Detail tiap fase: di PLAN.md, atau di `docs/phases/Pn-<nama>.md` jika lebih dari 5 fase. Tasks dengan path file, DoD berupa perintah, Out of scope, Handoff notes kosong.
4. `docs/DECISIONS.md`: keputusan teknis yang sudah ada di PRD/PLAN.
5. AGENTS.md: lengkapi bagian DI LUAR blok `repo-playbook:start/end` dengan stack, perintah test/lint/build, dan konvensi. Pertahankan aturan lama. Jangan ubah isi blok repo-playbook.
6. CLAUDE.md: pastikan ada `@AGENTS.md`. Bagian yang tumpang tindih dengan AGENTS.md dipindah ke AGENTS.md; sisakan aturan khusus Claude saja.
7. Commit: `docs: setup phased workflow`.

## Laporan akhir (maksimal 20 baris)

- temuan MINOR yang diperbaiki
- urutan eksekusi per gelombang (fase mana yang bisa paralel)
- fase pertama yang siap dikerjakan (lanjutkan dengan skill phase-start)
- ringkasan perubahan AGENTS.md/CLAUDE.md
- asumsi yang dibuat
