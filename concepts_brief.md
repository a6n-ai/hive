# Shown-line labels for the local score model

* **Author:** lawbringr
* **Date:** 2026-11-12

## Overview

Inscribe ranks memories with eight local weights. A prompt injects a few lines. This concept changes how those lines are labeled after the agent acts. It does not add a new kind of model, and it does not call a model on the hook.

One prompt and the next action that has command text form one episode. A shown line is a hit when every content word of that command appears as a whole word in the line's key or text. If at least one shown line is a hit, the other lines from that prompt are misses. If none is a hit, the shown lines stay unlabeled. The train does not store a memory and does not treat an unshown row as a weight hit. A correction or a forget beats a success. Each label stores a reason. Uploaded snapshots of one label-rule version are averaged together. Snapshots from another version are not.

A second review accepted one limit and blocked three others. A line that changed the action without containing the command's words has no local signal a weight update can use. Leaving it unlabeled is the rule, not a compromise. The open conflicts are when a later correction may rewrite a closed episode, whether a sibling miss may move all eight weights, and how rows and weights that already exist survive the new rule.

## Analysis

The first review's three objections are closed in the concept as follows. A hit requires every content word of the command, not one shared word or a prefix. Path tokens, error text, skill names, and prompt text do not create a hit. Sibling misses are written only when that stricter hit exists. The train does not insert a memory and does not count an unshown row as a weight hit. One prompt event id groups the shown lines. One transaction labels the group. The stored reason names which rule fired. A release averages one label-rule version, and the vote counts distinct prompts, capped at 500.

The second review says the wordless case cannot be fixed inside this labeler. A hit would treat a line that did not echo the command as reuse. A miss would treat a possible influence as failure. Both look the same when the command's words are absent. A synonym list or an embedding would be another model. The detached train does not add one.

Three conflicts remain.

A closed episode and a later correction cannot both be absolute. The concept says later actions do not relabel the episode, and it also says a correction or a forget beats a success. A correction that arrives after the next command has to rewrite the hit, or the hit stands and the precedence is false. If that hit is overturned, the sibling misses were written only because the hit existed. Leaving them in place punishes lines that no longer lost to a winner. The concept does not say which write wins, and it uses both "unlabeled" and `quiet` for the no-hit case. A second train cannot tell "not closed yet" from "closed with nothing to learn."

A line label is one bit, and the weights are eight numbers. One hit plus the other shown lines as misses means the misses outnumber the hit, and `learn` steps every weight that had a nonzero grade. A field that did not beat the text floor still moves if its grade was above zero. The review's safeguard is to let a sibling miss stay a stored reason and move only the fields that contained the command words, or to move no weights on a sibling miss. The author has not chosen which.

Rows already stored cannot take the new rule in place. They have no prompt id. Guessing one from timestamps can attach the wrong prompt. The live weight row was trained by the old overlap test. An old binary can still open unresolved rows and write the old 0 or 1. The safeguard is to close those unresolved rows without learning from them, start a new weight row under a key the old binary does not read, and write new labels where the old train's query cannot update them. The author has not accepted that migration.

Smaller gaps, not yet chosen: copy the key and text that were shown onto the outcome, so a later edit or forget cannot change the words the label sees. One train holds a lock and the other exits. A command with no content words is not a hit. The upload stays weights and counts, not command text.

Ideal result if those three conflicts are closed: the train learns only from stored reasons on lines that contain the command's words, a sibling miss does not flood the eight weights, a later correction has one legal effect on that episode, and the old weight row stays on the old rule.

## Goals vs. Non-Goals

* **In-Scope Goals:**
  * Keep the eight local weights and the detached train.
  * Group shown lines by the prompt event id, and close the group on the next action that has command text.
  * Mark a hit only when every content word of that command appears as a whole word in the line's key or text.
  * Mark the other lines from that prompt as misses only when at least one line in the group is such a hit.
  * Leave the group unlabeled when no line is a hit. Do not treat that as a weight update.
  * On a failure, correction, or forget, mark only the tied lines as misses. A correction or a forget beats a success.
  * Store the reason on the label row.
  * Put a label-rule version on each uploaded snapshot, and average only snapshots that share it. Count distinct prompts, capped at 500.
  * Run one train at a time, and label a prompt group in one transaction.
* **Out-of-Scope Non-Goals:**
  * A new model class, an embedding index, or a model call on the hook.
  * Uploading prompts, memory text, paths, or field grades.
  * Treating every shown line as a hit.
  * A sibling miss from one shared word, a prefix, a path token, error text, or the prompt text.
  * The train inserting a memory, inventing field grades, or counting an unshown row as a weight hit.
  * Averaging snapshots that were labeled under different rules.
  * A weight update for a shown line that does not contain the command's words.

## Alternatives Weighed

### Mark every shown line as a hit

* **Pros:** One label. No need to decide which line the command used.
* **Cons:** A line that was only sitting beside the useful line is trained as a success.
* **Reason for Rejection:** Rejected by author: count every shown line, but do not give them the same label.

### Sibling miss from any shared word

* **Pros:** The test is the one the ranker already uses.
* **Cons:** One prefix or one path token crowns a winner. Every install learns the same miss, and the median check ships it.
* **Reason for Rejection:** Rejected by author: a hit requires every content word of the command. Sibling misses exist only beside such a hit.

### Leave every non-overlapping line unlabeled, including when another line matched

* **Pros:** A miss stays reserved for failure, correction, or forget.
* **Cons:** The update never learns that a co-injected line was the wrong one to show when another line contained the command.
* **Reason for Rejection:** Rejected by author: keep the sibling miss, but only after a full-command hit. The second review reopened whether that miss may move all eight weights. That part is not rejected and is not accepted.

### Store the no-overlap action as a weight hit

* **Pros:** The action would be reinforced even when it shares no words with the lines that were shown.
* **Cons:** The row was never shown, so it has no field grades. The train would be a second writer.
* **Reason for Rejection:** Rejected by author: the train does not write the memory and does not count it as a weight hit.

### Credit a shown line that influenced the action without using its words

* **Pros:** A preference that changed the next command would get a hit.
* **Cons:** The episode looks the same when the line did nothing. The missing signal is a semantic judge.
* **Reason for Rejection:** Rejected by the second review's inventive lens, and left unlabeled by the author: no local observable distinguishes influence from non-influence, and another model class is out of scope.

### Replace the eight weights with another model

* **Pros:** Embeddings can match a line that influenced the agent without sharing words.
* **Cons:** Capture and prompt recall take no model call.
* **Reason for Rejection:** Rejected by author: the local model and the shipped model stay the eight weights.

## Key Decisions & Staging for Next Steps

Resolved:

* The model stays the eight weights. No model call on the hook.
* One prompt event and the next action with command text are one episode.
* A hit is a shown line whose key or text contains every content word of that command as a whole word.
* Sibling misses are written only when that episode has at least one hit. Otherwise the shown lines stay unlabeled and the weights do not move for that episode.
* The train does not insert memories and does not treat an unshown row as a weight hit.
* A shown line that does not contain the command's words stays unlabeled. That case is not a weight update.
* Uploaded snapshots carry a label-rule version. A release averages one version. The vote counts distinct prompts, capped at 500.

Not resolved:

* Whether a correction or a forget after the closing command may rewrite the episode, and what happens to sibling misses if the only hit is overturned.
* Whether a sibling miss moves all eight weights, only the fields that contained the command words, or no weights and remains an audit reason.
* Whether existing unresolved rows and the current weight row stay on the old rule, with the new rule starting from the shipped defaults in a separate weight row.

Next step: the author says which of those three to change. Another review waits on that answer. `/egm-draft` waits until the concept survives a review.

--------------------------------------------------------------------------------

*Drafted with EGM*
