---
name: phase-status
description: Laporan status fase project yang read-only dan singkat (fase done, in-progress, berikutnya, dan skill yang disarankan) dari docs/PLAN.md dan git. Pakai di awal sesi atau saat user tanya progres, status, atau "lanjut apa". Tidak mengubah file apa pun.
---

# phase-status

READ-ONLY: jangan mengubah, membuat, atau meng-commit file apa pun.

1. Jika `docs/PLAN.md` tidak ada, laporkan bahwa project belum di-setup, lalu sarankan:
   - phase-setup-new (belum ada kode), atau
   - phase-setup-existing (sudah ada kode)

   Lalu selesai.
2. Baca tabel fase di PLAN.md, `git status --short`, dan `git log --oneline -5`.
3. Laporkan maksimal 10 baris:
   - jumlah fase per status (todo / in-progress / review / done)
   - fase `in-progress` atau `review` saat ini, dan apakah working tree bersih
   - fase yang siap dikerjakan: semua fase di `Depends on`-nya `done`. Fase lain yang sedang `in-progress` tidak menghalangi, kecuali tercantum di `Depends on`.
   - fase yang bisa dikerjakan paralel (Parallel-safe), di worktree terpisah
   - kontradiksi di PLAN, jika ada (fase yang tercantum di `Depends on` dan `Parallel-safe with` sekaligus)
   - satu saran langkah berikutnya: phase-start `<ID>`, phase-resume `<ID>`, atau review/merge `<ID>`

Jangan menjalankan test, build, atau perintah yang lama.
