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
- semua fase di `Depends on` fase target berstatus `done`. Hanya kolom ini yang dihitung sebagai dependensi.
- DoD fase dependensi terakhir masih lulus (jalankan perintahnya)
- DoD fase target berupa perintah yang bisa dijalankan
- Tasks dan Out of scope cukup jelas untuk dikerjakan tanpa menebak
- checkout tempat bekerja bersih (lihat aturan fase lain di bawah)

Jika tidak: BERHENTI. Jelaskan masalahnya (maksimal 10 baris), usulkan perbaikannya, lalu tunggu user.

### Fase lain yang sedang `in-progress` atau `review`

Fase lain yang belum selesai BUKAN blocker, kecuali fase itu ada di `Depends on` fase target. Jangan berhenti hanya karena ada fase lain yang belum selesai.

- **Fase lain itu ada di `Depends on`:** berhenti. Ini dependensi yang belum `done`.
- **Fase lain itu tidak ada di `Depends on`:** lanjut, dengan pengaturan berikut.
  - Kerjakan fase target di branch atau worktree terpisah dari fase lain itu, supaya commit tidak tercampur. Pilih salah satu:
    - thread worktree di T3 Code
    - `delegate_task` dengan `isolation: "worktree"`
    - `git switch -c phase/<ID>` jika checkout saat ini bersih
  - Jika keduanya saling `Parallel-safe`: lanjut tanpa peringatan.
  - Jika tidak saling `Parallel-safe`: tetap lanjut, tapi sebutkan di laporan file atau area yang berisiko konflik.
- **Checkout saat ini kotor karena perubahan fase lain:** jangan berhenti, dan jangan menyentuh perubahan itu. Kerjakan fase target di worktree terpisah. Jika worktree tidak bisa dibuat, berhenti dan sarankan user membuka thread worktree baru.
- **Fase tercantum di `Depends on` dan `Parallel-safe with` sekaligus:** ini kontradiksi di PLAN. Berhenti, lalu usulkan pemecahan fase. Contoh: bagian yang sudah selesai menjadi `P3` (`done`), sisanya menjadi `P3b`.

## 3. Kerjakan

1. Set status `in-progress` di PLAN.md, lalu commit di branch atau worktree yang dipakai untuk fase ini.
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
