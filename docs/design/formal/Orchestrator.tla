---------------------------- MODULE Orchestrator ----------------------------
(***************************************************************************)
(* An abstract TLA+ model of the Age of Agents merge path: the            *)
(* single-writer, Gate-verified merge queue with idempotency keys and the  *)
(* optional human-in-the-loop approval gate (ADR 002, 008).               *)
(*                                                                         *)
(* It model-checks the same load-bearing safety properties the Go         *)
(* invariant harness asserts dynamically over event histories             *)
(* (internal/invariant):                                                   *)
(*                                                                         *)
(*   I1  MergeImpliesVerified  - nothing merges unless the Gate passed      *)
(*   I2  MergedAtMostOnce      - single writer; a ticket merges at most once *)
(*   I4  NoDuplicateMergedKey  - no two merged tickets share an idem. key    *)
(*   AG  ApprovalGate          - a parked proposal merges only once approved *)
(*                                                                         *)
(* `main` is always green because Commit (the only writer) fires only from  *)
(* a verified state and a failed Gate Rejects without writing — so          *)
(* MergeImpliesVerified is exactly the "main never red" guarantee (ADR 002).*)
(***************************************************************************)
EXTENDS Naturals, FiniteSets, Sequences

(***************************************************************************)
(* The concrete model checked here: three tickets, where t1 and t2 share   *)
(* an idempotency key ("ka") and t3 has its own ("kb"), with the approval   *)
(* gate enabled. Sharing a key is what exercises NoDuplicateMergedKey: at   *)
(* most one of t1/t2 may ever merge. Vary these definitions to explore      *)
(* other shapes (see README.md).                                           *)
(***************************************************************************)
Tickets         == {1, 2, 3}
Key             == [t \in Tickets |-> IF t <= 2 THEN "ka" ELSE "kb"]
RequireApproval == TRUE

VARIABLES status,           \* [Tickets -> lifecycle stage]
          merged,           \* Seq(Tickets) : the linearized single-writer merge order
          verifiedOnce,     \* tickets whose proposal ever passed the Gate
          approved          \* tickets a human approved

vars == <<status, merged, verifiedOnce, approved>>

Range(s)  == { s[i] : i \in DOMAIN s }
MergedSet == Range(merged)

TypeOK ==
  /\ status \in [Tickets -> {"pending","proposed","verified","awaiting","merged","failed"}]
  /\ merged \in Seq(Tickets)
  /\ verifiedOnce \subseteq Tickets
  /\ approved \subseteq Tickets

Init ==
  /\ status = [t \in Tickets |-> "pending"]
  /\ merged = << >>
  /\ verifiedOnce = {}
  /\ approved = {}

\* A worker submits a candidate change.
Propose(t) ==
  /\ status[t] = "pending"
  /\ status' = [status EXCEPT ![t] = "proposed"]
  /\ UNCHANGED <<merged, verifiedOnce, approved>>

\* The Gate passes on the candidate (a dry run that does not touch main).
Verify(t) ==
  /\ status[t] = "proposed"
  /\ status' = [status EXCEPT ![t] = "verified"]
  /\ verifiedOnce' = verifiedOnce \cup {t}
  /\ UNCHANGED <<merged, approved>>

\* The Gate fails; main is rolled back and stays green.
Reject(t) ==
  /\ status[t] = "proposed"
  /\ status' = [status EXCEPT ![t] = "failed"]
  /\ UNCHANGED <<merged, verifiedOnce, approved>>

\* A verified proposal is parked for a human decision.
RequestApproval(t) ==
  /\ RequireApproval
  /\ status[t] = "verified"
  /\ t \notin approved
  /\ status' = [status EXCEPT ![t] = "awaiting"]
  /\ UNCHANGED <<merged, verifiedOnce, approved>>

Approve(t) ==
  /\ status[t] = "awaiting"
  /\ status' = [status EXCEPT ![t] = "verified"]
  /\ approved' = approved \cup {t}
  /\ UNCHANGED <<merged, verifiedOnce>>

Deny(t) ==
  /\ status[t] = "awaiting"
  /\ status' = [status EXCEPT ![t] = "failed"]
  /\ UNCHANGED <<merged, verifiedOnce, approved>>

\* Commit can fire only for a verified, (approved if required) proposal whose
\* idempotency key has not already merged. It is the single writer to main.
CanMerge(t) ==
  /\ status[t] = "verified"
  /\ (RequireApproval => t \in approved)
  /\ \A m \in MergedSet : Key[m] # Key[t]

Commit(t) ==
  /\ CanMerge(t)
  /\ status' = [status EXCEPT ![t] = "merged"]
  /\ merged' = Append(merged, t)
  /\ UNCHANGED <<verifiedOnce, approved>>

Next ==
  \E t \in Tickets :
       Propose(t) \/ Verify(t) \/ Reject(t)
    \/ RequestApproval(t) \/ Approve(t) \/ Deny(t)
    \/ Commit(t)

Spec == Init /\ [][Next]_vars

(***************************************************************************)
(* Safety invariants (the model checker proves these hold in every state). *)
(***************************************************************************)

\* I1: every merged ticket passed the Gate first  =>  main is never red.
MergeImpliesVerified == MergedSet \subseteq verifiedOnce

\* I2: the merge log is a single writer with no double-merges.
MergedAtMostOnce ==
  \A i, j \in DOMAIN merged : (i # j) => (merged[i] # merged[j])

\* I4: no two distinct merged tickets share an idempotency key (no step repetition).
NoDuplicateMergedKey ==
  \A t1, t2 \in MergedSet : (Key[t1] = Key[t2]) => (t1 = t2)

\* AG: with the approval gate on, nothing merges without explicit approval.
ApprovalGate == RequireApproval => (MergedSet \subseteq approved)

=============================================================================
