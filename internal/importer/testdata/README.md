# Importer test fixtures

`snapshot/` is a GitHub Classroom snapshot in the exact on-disk layout
`pkg/adapter/github/classroom` writes, used to test the importer without a
network.

Its **schema** was taken field-for-field from a real capture (see
`docs/ghc-import.md`); its **content** is entirely synthetic — `CS-101-F26`,
`student01`…`student03`. No real student identifier appears here, and none should
ever be committed.

It deliberately covers the awkward cases observed in real data:

- a past deadline (`hw-01`) and a future one (`hw-02`), to exercise retroactive
  lock suppression on one but not the other
- grades present on some repos and `null` on most, which is what GitHub actually
  returns
- a group assignment (`final-project`) with a two-member team and a solo team,
  whose repository names are student-chosen and not derivable from a username
- an assignment with no starter-code repository
- an unparseable grade (`"n/a"`) and an acceptance with no student accounts, both
  of which must warn rather than import something wrong
