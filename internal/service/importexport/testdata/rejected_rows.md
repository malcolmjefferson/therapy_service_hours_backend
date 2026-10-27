# Clinical Hours Ledger

## Clients / Groups
- Pseudonym A
- ============================================================================ too long ============================================================================
- —

## Entries
| Date | Group | Category | Client | Hours | On-site | Notes |
|---|---|---|---|---|---|---|
| 2026-03-02 | Direct Service | Individual Counseling | Pseudonym A | 1 | Yes | valid row |
| 2026-02-30 | Direct Service | Individual Counseling | Pseudonym A | 1 | Yes | impossible date |
| 2026-03-03 | Direct Service | Individual Counseling | Pseudonym A | 0 | Yes | zero hours |
| 2026-03-03 | Direct Service | Individual Counseling | Pseudonym A | 25 | Yes | too many hours |
| 2026-03-03 | Direct Service | Individual Counseling | Pseudonym A | 1e2 | Yes | exponent |
| 2026-03-04 | Teaching | Individual Counseling | Pseudonym A | 1 | Yes | unknown group |
| 2026-03-04 | Direct Service |  | Pseudonym A | 1 | Yes | no category |
| 2026-03-05 | Direct Service | Individual Counseling | Pseudonym A | 1 | Maybe | bad on-site |
| 2026-03-05 | Direct Service | Individual Counseling | Pseudonym A | 1 | Yes |
| 2026-03-05 | Direct Service | Individual Counseling | Pseudonym A | 1 | Yes | too | many | cells |
| 2026-03-06 | Direct Service | Individual Counseling | x' OR '1'='1 | 1 | Yes | DROP TABLE entries -- is just text |
| 2026-03-07 | Direct Service | Individual Counseling | =HYPERLINK("x") | 1 | Yes | =1+1 stays text |
