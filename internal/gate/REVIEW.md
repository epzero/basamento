# Review guide: the gate

High risk: the gate decides what "done" means for every agent. A weaker gate
lets bad changes through silently.

- No check becomes more permissive without a reason in the commit body and a
  person's acceptance.
- New checks have tests that show them failing on a bad input.
- Commands stay deterministic and offline, except where CI needs history
  (`redgreen`).
- Error messages say what to do next.
