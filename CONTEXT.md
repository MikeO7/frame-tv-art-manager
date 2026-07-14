# Frame TV Art Management

Frame TV Art Management keeps a local artwork collection reconciled with the
user-managed artwork on one or more Samsung Frame TVs while preserving data
when either side cannot be observed reliably.

## Language

**Artwork Collection**:
The durable set of supported local images that expresses what the manager
should maintain on each configured TV.
_Avoid_: Artwork folder, local files, source catalog

**Collection Snapshot**:
A complete, trustworthy observation of the Artwork Collection at a point in
time. An incomplete source resolution is not a Collection Snapshot.
_Avoid_: File list, catalog result

**Artwork Digest**:
The full SHA-256 digest of committed artwork bytes. It identifies an exact
local artwork version for equality, deduplication, and Reconciliation.
_Avoid_: Filename hash, short hash, image ID

**Origin Key**:
A stable provider-owned identity, upload identity, or operator origin that
records how artwork entered the Artwork Collection and whether it may be
source-pruned.
_Avoid_: Filename prefix, download index

**TV Inventory**:
The trustworthy set of user-managed artwork observed on a configured TV.
_Avoid_: Remote files, content list

**Owned Artwork**:
Artwork on a TV whose content identity is durably associated with an image in
the Artwork Collection by this manager.
_Avoid_: Known image, tracked file

**Unknown Artwork**:
Artwork in the TV Inventory that is not Owned Artwork.
_Avoid_: Untracked image, foreign file

**TV State**:
The observed power and Art Mode condition that determines whether a TV can be
safely reconciled. TV State is known only when the current observation
succeeds.
_Avoid_: TV status, mode flag

**Sync Cycle**:
One attempt to obtain a Collection Snapshot and reconcile every configured TV
whose TV State and TV Inventory are known.
_Avoid_: Run, loop iteration

**Reconciliation**:
The convergence of a known TV Inventory toward a Collection Snapshot under the
configured preservation and removal policy.
_Avoid_: Sync logic, update process
