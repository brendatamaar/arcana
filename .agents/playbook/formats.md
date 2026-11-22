# Format dokumen workflow per fase

File ini dikelola oleh repo-playbook (ditimpa saat install ulang). Jangan edit di project.

## Lokasi

```text
docs/PRD.md          # apa & kenapa (ditulis user)
docs/PLAN.md         # arsitektur ringkas + tabel status fase
docs/DECISIONS.md    # log keputusan teknis
docs/phases/Pn-<nama>.md   # hanya jika lebih dari 5 fase; jika tidak, detail fase di PLAN.md
```

## PLAN.md

```markdown
# PLAN
## Arsitektur ringkas
<5-15 baris: komponen utama, data flow, library kunci>

## Fase
| ID | Nama | Status | Depends on | Parallel-safe with |
|----|------|--------|------------|--------------------|
| P1 | Foundation | done | - | - |
| P2 | Auth | in-progress | P1 | P3 |
```

Status yang sah: `todo`, `in-progress`, `review`, `done`.

## Detail fase (di PLAN.md sebagai `## P2 - Auth`, atau di docs/phases/P2-auth.md)

```markdown
# P2 - Auth
Status: todo
Depends on: P1
Parallel-safe with: P3

## Goal
<1-2 kalimat hasil yang terlihat>

## Tasks
- [ ] <task> (<path file>)

## Definition of Done
- `<perintah test>` lulus, `<perintah lint>` bersih
- <skenario konkret yang dites>

## Out of scope
<apa yang TIDAK dikerjakan, dan fase mana yang mengerjakannya>

## Handoff notes
<diisi agent: keputusan, helper penting, jebakan, sisa pekerjaan>
```

Aturan:
- DoD WAJIB berupa perintah yang bisa dijalankan. Jika test belum ada, task pertama fase = "tulis test X".
- Satu fase = satu sesi kerja, diff yang masih bisa direview dalam 15-20 menit.
- Fase `done` pada project lama cukup Goal + Handoff notes.
- `Depends on` hanya berisi fase yang HARUS `done` sebelum fase ini dimulai. Fase lain yang sedang `in-progress` tidak menghalangi.
- Sebuah fase tidak boleh tercantum di `Depends on` dan `Parallel-safe with` sekaligus.
- Jika fase lain hanya butuh bagian yang sudah selesai dari sebuah fase, pecah fase itu (`P3` = bagian yang `done`, `P3b` = sisanya). Jangan memakai tanda bebas seperti "(sisa)".
- Dua fase yang berjalan bersamaan dikerjakan di branch atau worktree terpisah.

## DECISIONS.md

```markdown
| Tanggal | Keputusan | Alasan | Fase |
|---------|-----------|--------|------|
| 2026-11-22 | Session via cookie | Tanpa state server | P2 |
```

## AGENTS.md dan CLAUDE.md

- Blok di antara `<!-- repo-playbook:start -->` dan `<!-- repo-playbook:end -->` dikelola installer. Jangan diubah.
- Di luar blok itu, AGENTS.md berisi: stack, perintah test/lint/build, konvensi project. Target total di bawah 60 baris.
- CLAUDE.md berisi `@AGENTS.md` + aturan khusus Claude saja. Bagian umum dipindah ke AGENTS.md.
