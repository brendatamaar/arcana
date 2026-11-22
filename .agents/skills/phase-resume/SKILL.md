---
name: phase-resume
description: Lanjutkan SATU fase yang terhenti di tengah jalan (sesi putus, usage limit, ganti provider) - rekonstruksi progres dari repo, selesaikan task tersisa sampai DoD lulus, review, lalu berhenti tanpa lanjut ke fase berikutnya. Pakai saat user minta lanjutkan/resume fase.
---

# phase-resume

Fase target: ID fase dari permintaan user. Jika tidak disebut, ambil fase berstatus `in-progress` di PLAN.md. Jika ada lebih dari satu, tanyakan user.
Selesaikan HANYA fase ini. JANGAN memulai fase berikutnya.

## 1. Rekonstruksi

Baca AGENTS.md, `docs/PLAN.md`, `docs/DECISIONS.md`, dan detail fase target (task yang sudah dicentang + Handoff notes).

Lalu cek kondisi repo:
- `git status`
- `git diff`
- `git log --oneline -15`
- jalankan perintah DoD fase ini untuk melihat posisi saat ini

Jika Handoff notes bertentangan dengan isi repo, ikuti repo dan catat perbedaannya.

## 2. Gate

Lanjut OTOMATIS hanya jika SEMUA syarat terpenuhi:
- task yang selesai dan yang tersisa jelas dari bukti repo
- perubahan uncommitted (jika ada) jelas merupakan bagian dari task fase ini
- tidak ada konflik antara Handoff notes, PLAN, dan isi repo yang mengubah scope

Jika tidak: BERHENTI. Laporkan task selesai, pekerjaan setengah jadi, task tersisa, dan hal yang perlu diputuskan (maksimal 15 baris), lalu tunggu user.
Perubahan uncommitted yang tidak jelas tujuannya JANGAN pernah dibuang.

## 3. Selesaikan

1. Rapikan dan commit pekerjaan setengah jadi yang valid terlebih dahulu.
2. Kerjakan task tersisa sesuai urutan, hanya dalam scope. Commit kecil, centang task, dan update Handoff notes setiap kali satu task selesai.
3. Pekerjaan paralel, review silang, atau eksplorasi besar: ikuti skill phase-routing.
4. Selesai = DoD lulus (jalankan sendiri). DoD gagal 3x berturut-turut: BERHENTI dan laporkan penyebabnya.
5. Di akhir:
   - lengkapi Handoff notes
   - catat keputusan baru di DECISIONS.md
   - set status `review`, lalu commit
6. Review:
   - jika tersedia perintah code review (mis. `/code-review` di Claude Code), jalankan dengan level medium
   - jika tidak, review diff sendiri: bug, edge case, pelanggaran scope
   - perbaiki temuan kecil

## Laporan akhir (maksimal 15 baris)

Posisi saat dilanjutkan, apa yang diselesaikan, hasil DoD, temuan review, dan perbedaan Handoff notes vs repo (jika ada). JANGAN memulai fase berikutnya.
