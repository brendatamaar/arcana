---
name: phase-start
description: Mulai SATU fase yang belum dikerjakan sesuai docs/PLAN.md - cek dependensi dan DoD, kerjakan hanya scope fase sampai DoD lulus, review, lalu berhenti tanpa lanjut ke fase berikutnya. Pakai saat user minta mulai/kerjakan fase (mis. "mulai P3" atau "kerjakan fase berikutnya").
---

# phase-start

Fase target: ID fase dari permintaan user (mis. `P3`). Jika tidak disebut, pilih fase `todo` pertama yang semua dependensinya `done`.
Kerjakan HANYA fase ini. JANGAN memulai fase berikutnya.

## 1. Persiapan

Baca AGENTS.md, `docs/PLAN.md`, `docs/DECISIONS.md`, detail fase target, dan Handoff notes fase dependensinya. Cek `git status` dan `git log --oneline -10`.

## 2. Gate

Mulai OTOMATIS hanya jika SEMUA syarat terpenuhi:
- semua dependensi fase target berstatus `done`
- DoD fase dependensi terakhir masih lulus (jalankan perintahnya)
- working tree bersih
- DoD fase target berupa perintah yang bisa dijalankan
- Tasks dan Out of scope cukup jelas untuk dikerjakan tanpa menebak

Jika tidak: BERHENTI. Jelaskan masalahnya (maksimal 10 baris), usulkan perbaikannya, lalu tunggu user.

## 3. Kerjakan

1. Set status `in-progress` di PLAN.md, lalu commit.
2. Kerjakan task sesuai urutan, hanya dalam scope. Hal di luar scope dicatat di Handoff notes.
3. Commit kecil per task. Setiap kali satu task selesai, centang task dan update Handoff notes.
4. Pekerjaan paralel, review silang, atau eksplorasi besar: ikuti skill phase-routing.
5. Selesai = DoD lulus (jalankan sendiri). DoD gagal 3x berturut-turut: BERHENTI dan laporkan penyebabnya.
6. Di akhir:
   - lengkapi Handoff notes
   - catat keputusan baru di DECISIONS.md
   - set status `review`, lalu commit
7. Review:
   - jika tersedia perintah code review (mis. `/code-review` di Claude Code), jalankan dengan level medium
   - jika tidak, review diff sendiri: bug, edge case, pelanggaran scope
   - fase penting (data, keamanan, uang): tambah review silang via phase-routing
   - perbaiki temuan kecil

## Laporan akhir (maksimal 15 baris)

Apa yang dikerjakan, hasil DoD, temuan review (diperbaiki atau tidak), dan hal di luar scope yang dicatat. JANGAN memulai fase berikutnya.
