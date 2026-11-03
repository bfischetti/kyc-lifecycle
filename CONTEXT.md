# KYC/KYB Lifecycle

Tracks a merchant or individual from first contact through identity verification, screening, and compliance decision, with a complete audit trail of every status change.

## Language

### Subjects

**Entity**:
The merchant (business) or person (individual) being verified. Has exactly one **Verification Status** at any time.
_Avoid_: Customer, merchant, account, subject

**Verification Status**:
Where an **Entity** stands: unverified (nothing submitted), pending review (a **Review Cycle** is open), verified (approved; terminal), or rejected (may be resubmitted). Every change is recorded in the audit trail.
_Avoid_: State, KYC status

### Actors

**Compliance Officer**:
A person who screens **Entities**, makes **Compliance Approvals** and **Compliance Rejections**, and reads the audit trail. Sees everything.

**Support Agent**:
A person who onboards **Entities** and attaches **Documents**. Cannot screen or decide, and only sees the **Redacted View**.
_Avoid_: Operator, admin

**System**:
The non-human actor behind automated work: running **Checks**, **System Rejections**, and downstream notification. Every automated status change is attributed to it.

**Redacted View**:
An **Entity** with its **Documents** and **Checks** as a **Support Agent** sees it: **Evidence Vault** keys, document contents, and raw provider payloads are withheld.

### Evidence

**Document**:
A piece of proof an **Entity** submitted (passport, national ID, business registration, proof of address), with an optional expiry date. Its bytes live in the **Evidence Vault**.
_Avoid_: Upload, file, attachment

**Evidence Vault**:
The private store holding the actual bytes of every **Document**. Documents are referenced by an opaque key that reveals nothing about the **Entity** and is never shown to callers.
_Avoid_: Bucket, file store, storage_ref

**Evidence Access**:
A single act of opening a **Document**'s contents, always attributed to a person and recorded. Only a **Compliance Officer** may access evidence.
_Avoid_: Download, view

**Upload Intent**:
A declared, not-yet-delivered **Document**: the caller has stated its type, size, content type and fingerprint, and has a short-lived permission to deliver exactly those bytes to the **Evidence Vault**. Has no effect on the **Entity**.

**Completed Upload**:
A **Document** whose bytes are confirmed present in the **Evidence Vault** and matching what was declared. Only Completed Uploads count toward **Required Documents** and trigger status changes.
_Avoid_: Uploaded document (ambiguous with an Upload Intent)

**Expired Upload**:
An **Upload Intent** never completed before its permission lapsed. Permanently dead; the caller must declare a new one.

**Required Documents**:
The **Document** types an **Entity** must have as **Completed Uploads**, unexpired, in the current **Review Cycle** before approval. Individual: passport or national ID. Business: business registration. Proof of address is never required.

**Document Review Status**:
Whether a **Document** was accepted or rejected. Not set per document; it is decided as a whole by the **Review Cycle** outcome. Approval accepts the cycle's pending Documents, and any rejection rejects them.

### Screening

**Check**:
A single screening result from a provider for one **Entity** and one **Check Type**. Immutable once recorded; re-running produces a new Check.
_Avoid_: Verification, screening result

**Check Type**:
The kind of screening: sanctions screening, PEP screening, or address verification.

**Required Checks**:
The set of **Check Types** an **Entity** must pass before it can be approved, determined by entity type. Individual: sanctions + PEP. Business: sanctions + address verification.

**Effective Result**:
The result of the most recent **Check** of a given **Check Type** for an **Entity** within the current **Review Cycle**. Earlier Checks, including those from prior cycles, are history only and do not count toward approval.
_Avoid_: Current check, latest status

**Review Cycle**:
The period from an **Entity** entering pending review until a decision (approval or rejection). Each **Resubmission** starts a new cycle, and screening must be redone within it.

**Resubmission**:
A **Completed Upload** on a rejected **Entity**, which returns it to pending review and starts a new **Review Cycle**. Verified Entities cannot be resubmitted.

**Hard Fail**:
A **Check** whose result is fail. Causes the **Entity** to be rejected automatically by the system, without a human decision.

**Manual Review Result**:
A **Check** whose result needs a human look. Blocks approval; it is resolved either by a **Compliance Rejection** or by re-running the Check.
_Avoid_: Soft fail, referral

**Screening Provider**:
The external vendor that performs a **Check**. In this project a deterministic mock stands in for real vendors.
_Avoid_: Vendor, integration

**Provider Error**:
A failure to obtain a screening answer (timeout, outage). Not a **Check**: it records nothing about the **Entity** and never causes a rejection.
_Avoid_: Failed check (that is a **Hard Fail**)

### Decisions

**Compliance Approval**:
A compliance officer's decision to move an **Entity** to verified. Allowed only when every **Required Check** has an **Effective Result** of pass and the **Required Documents** are present and unexpired. Always carries a written reason and must be based on the **Entity**'s current **Entity Version**. There is no override and no second-officer confirmation.

**Compliance Rejection**:
A compliance officer's decision to move an **Entity** to rejected, possible at any point while it is pending review. Always carries a written reason and must be based on the current **Entity Version**.

**System Rejection**:
A rejection caused by a **Hard Fail**, attributed to the system rather than a person.

**Stale Decision**:
A **Compliance Approval** or **Compliance Rejection** made against an **Entity Version** that is no longer current. Always refused; the officer must look again.

### Notification

**Status Change Notification**:
The message telling **Downstream Consumers** that an **Entity**'s status changed. Contains identifiers and the transition only, never document contents or provider payloads. May be delivered more than once.
_Avoid_: Webhook, event (too generic)

**Entity Version**:
A counter that increases by one with every status change of an **Entity**. Lets a **Downstream Consumer** discard a **Status Change Notification** older than one it has already seen.

**Downstream Consumer**:
Any system outside this service that acts on **Status Change Notifications** (e.g. enabling payouts for a verified merchant).

## Relationships

- An **Entity** has many **Checks**; for each **Check Type** at most one is the **Effective Result**.
- An **Entity** is approvable only when every **Required Check** has an **Effective Result** of pass.
- An **Entity** has one or more **Review Cycles** over its life; at most one is open at a time.
- A **Document** begins as an **Upload Intent** and becomes either a **Completed Upload** or an **Expired Upload**.
- Every change of **Verification Status** increments the **Entity Version** and produces exactly one audit record and one **Status Change Notification**.

## Example dialogue

> **Dev:** A support agent uploaded a passport for a rejected merchant, but the status didn't move. Bug?
> **Compliance:** Did the upload complete? Asking for an upload link is only an **Upload Intent**, and nothing changes until it's a **Completed Upload**. Once it completes, that's a **Resubmission**: the **Entity** goes back to pending review and a new **Review Cycle** opens.
> **Dev:** And the sanctions pass from last week still counts?
> **Compliance:** No. **Effective Results** only come from the current cycle, so screening is redone. If sanctions comes back fail, that's a **Hard Fail** and a **System Rejection**. If the provider just times out, that's a **Provider Error**: nothing is recorded and we retry.
> **Dev:** And when I approve, the system says my decision is stale?
> **Compliance:** Someone changed the **Entity Version** while you were looking. That's a **Stale Decision**. Reload and decide again.
