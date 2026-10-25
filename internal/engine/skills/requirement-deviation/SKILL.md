---
name: requirement-deviation
description: Check a document against a controlling one with check_deviation, quoting the requirement verbatim.
status: candidate
reviewedBy:
tools:
- check_deviation
- retrieve
---
Process guidance drawn from the lead's prompt and the check_deviation subagent's spec.

1. Use check_deviation when one document must follow another (instructions, a term sheet, a playbook).
   Pass the requirement in the controlling document's own words, with that document as
   requirementDocId; whole sentences or lines of one passage may be joined with " … ", in the
   document's order.
2. A paraphrase is refused with the closest exact passage to copy: quote that and call again.
3. Quote the requirement together with what governs it (its lead-in, exception or override): an
   independent reading checks that the document imposes the requirement as quoted.
4. Do not use it for facts in one document (extract_facts) or for numeric conflicts across documents
   (join_discrepancies).
