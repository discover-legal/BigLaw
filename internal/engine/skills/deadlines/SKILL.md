---
name: deadlines
description: Compute every deadline with numbers date_add from a verified trigger date, never by hand.
status: candidate
reviewedBy:
tools:
- numbers
- draft_section
---
Process guidance drawn from the lead's prompt and the numbers tool's spec; no legal rule of its own.

1. Find the trigger date as a verified finding value (extract_facts or the sweep), and the period the
   record states for it.
2. Call numbers op calc with fn date_add: the trigger as {"ref": "F12.v1"} exactly as results show it,
   the period as a literal with its unit ("21" with unit "days"). Pass holidays (even []) to roll a
   result that lands on a weekend or one of those holidays to the next day that is not, as FRCP 6(a)
   does; the worksheet shows the raw date too.
3. A negative count counts back from an event (a hearing). date_add by months or years clamps to the
   target month's last day; date_add refuses "business days" (use fn business_days).
4. Draft the section with the worksheet as its handle ({W…}), so Go renders the computed date and
   finish can trace it. Never type a computed date into a draft.

The reference file examples.json holds the numbers calls the tool's own spec gives as examples.
