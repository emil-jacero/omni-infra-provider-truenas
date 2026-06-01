# M3 Social Posts — Control-plane OOM war story

Distribution pack for `06-control-plane-oom-war-story.md`. LinkedIn long-form + X thread + per-Community variants. Matches voice/format conventions in `_shared/linkedin-drumbeat.md` and `_shared/x-communities.md`.

Live URL placeholder: `https://dev.to/cliftonz/<cp-oom-slug>`

UTM convention per `_shared/utm-conventions.md`:
- LinkedIn: `?utm_source=linkedin&utm_medium=social&utm_campaign=cp-oom-2026-08`
- X main thread: `?utm_source=x&utm_medium=social&utm_campaign=cp-oom-2026-08`
- X Communities: `?utm_source=x&utm_medium=community-<name>&utm_campaign=cp-oom-2026-08`

---

## LinkedIn — long-form (single post, link in first comment)

**Post day**: Tuesday or Wednesday 9–11am ET. Slot into the M3 cadence as a bonus drumbeat post after the sizing post lands — this is the field-report companion to it.

**Hook** (first 2 lines, the truncated preview):
> My homelab Kubernetes cluster broke in a way I'd never seen before, and the bug wasn't in Kubernetes.
> It was in a number I picked on the day I built the cluster and never thought about again.

**Body**:

> A Talos control-plane VM I provisioned with 12 GB of RAM walked itself into memory exhaustion over four and a half days. Nothing dramatic — no leak, no runaway pod. Just kube-apiserver + etcd + controller-manager + scheduler working sets growing the way they always do, until MemAvailable got too low and the wheels came off.
>
> Symptoms, in the order I noticed them:
>
> → A Deployment spec change applied cleanly all the way through Crossplane, ArgoCD, and the child Application, but no new pod ever appeared. observedGeneration was frozen one behind generation.
> → kubectl was flaky. One in three calls returned "the server was unable to return a response in the time allotted."
> → kube-scheduler in CrashLoopBackOff with 93 restarts. kube-controller-manager flapping with 83.
> → etcd's own health probe oscillating OK / "context deadline exceeded" roughly every 60 seconds.
>
> The first instinct was etcd. It's almost always etcd. The DB was 135 MB with no alarms. Defrag would have reclaimed nothing.
>
> The actual cause was a layer below etcd: memory pressure. Once MemAvailable got low enough, Talos's userspace OOM controller started SIGKILLing best-effort cgroups in a loop (kube-proxy was the visible victim, but it wasn't the cause — it was just the lowest-QoS thing on the node). The reclaim stalls blew etcd's fsync latency budget. etcd flapping starved the apiserver. The starved apiserver meant scheduler and controller-manager couldn't sync informer caches or hold their leader-election leases. No scheduler + no controller-manager = no rollouts, cluster-wide. One under-sized control-plane VM produced a full control-plane brownout that looked like every Kubernetes bug at once.
>
> The instinct people will reach for next is swap. Don't.
>
> One: Talos doesn't support swap, by design. There's no shell, no swapon, no machine-config field. SwapTotal: 0 is not a setting you forgot to flip.
>
> Two: even if you could, swap on an etcd node is an upstream-documented anti-pattern. etcd's performance is dominated by fsync latency. The instant pages get swapped, your "context deadline exceeded" flapping becomes permanent. You convert hard OOM kills into sustained latency death. Strictly worse.
>
> The fix was more RAM (12 → 24 GB) and a planned migration off single-control-plane. There is no other lever.
>
> Three things I'd tell past-me, and anyone running a similar setup:
>
> 1. The default 2 GB control plane is fine on day one and a bomb on day thirty. Crossplane is the specific tripwire: once it's installed on a single-CP cluster, treat 16 GB as the floor — not 4 or 8. HA (3 CP) spreads the load and 8 GB per replica is enough for the same workload. Size for the working set you'll have in a month, not the one you have in an hour.
>
> 2. On Talos specifically: there is no swap backstop. Size for the working set you'll have in a month, not the one you have in an hour.
>
> 3. On any hypervisor: turn memory ballooning OFF for control-plane VMs. The hypervisor reclaiming guest pages under host pressure produces exactly the etcd fsync-stall failure mode in this story, and it's invisible from inside the guest.
>
> Full write-up — including the talosctl commands I wish I'd known to reach for first, the rule of thumb for telling this apart from a real etcd bug, and the concrete asks I've sent to the provider I maintain — in the comments.

**First comment**:
> Full post: <link>
> Companion piece (the sizing rules of thumb you want *before* you hit this): <sizing-post link>
> Repo: https://github.com/bearbinary/omni-infra-provider-truenas

**Hashtags**: `#Kubernetes #Talos #TrueNAS #Homelab #SelfHosted`

**Engagement plan**: reply to every comment for the first 4 hours. Likely questions to be ready for — "why 24 not 16," "what about cgroupv2 memory.high," "would Cilium reduce CP load," "have you opened an upstream Talos issue."

---

## X — main thread (8 posts, single thread)

**Posting time**: Tuesday or Wednesday 8–10am ET. Drop into your main feed first; cross-post to Communities (below) after the thread is live and engagement has settled (~2–3 hours).

```
1/8
A Talos control-plane VM with 12 GB RAM brought my whole Kubernetes
cluster to its knees this week.

Symptoms looked like every bug at once.

Cause was a number I picked on day one and never thought about again.

Story + the diagnostic path 👇

2/8
The visible symptoms:

→ Deployment spec change applied everywhere, no new pod ever appeared
→ kubectl flaky: "the server was unable to return a response"
→ kube-scheduler CrashLoopBackOff (93 restarts)
→ kube-controller-manager flapping (83)
→ etcd health probe: OK ↔ "context deadline exceeded" every ~60s

3/8
First instinct: etcd is bloated, needs defrag.

  talosctl -n $N etcd status
  DB SIZE: 135 MB. No alarms.

Defrag would have reclaimed nothing.

etcd is fine. Something *underneath* etcd is starving it.

4/8
Layer below:

  talosctl -n $N read /proc/meminfo
  MemAvailable: 1.4 GiB of 11.65
  SwapTotal:    0

  talosctl -n $N dmesg | grep -i oom
  [talos] OOM controller triggered
  [talos] Sending SIGKILL to cgroup .../besteffort/kube-proxy...
  (looping)

It's RAM.

5/8
The cascade, once MemAvailable got low:

mem pressure → Talos OOMs best-effort cgroups in a loop
       ↓
reclaim stalls → etcd misses fsync deadlines → health flap
       ↓
flapping etcd → slow apiserver
       ↓
slow apiserver → scheduler/cm can't sync caches or hold leases
       ↓
cluster-wide rollout + scheduling freeze

6/8
Next instinct: "just add swap."

DON'T.

a) Talos has no swap, by design. No shell, no swapon, no config field.
   SwapTotal: 0 is not a setting you forgot.

b) Swap on an etcd node is an upstream-documented anti-pattern.
   You'd turn hard OOMs into permanent fsync latency.

7/8
The fix has no shortcut:

→ More RAM (12 → 24 GB)
→ Migrate off single-control-plane
→ On any hypervisor: ballooning OFF for CP VMs. Host-side
  page reclaim on an etcd node produces this exact failure mode,
  invisibly from inside the guest

8/8
Rule of thumb I'm keeping:

flapping etcd health
+ tiny healthy DB
+ low MemAvailable
+ zero swap
= under-sized control-plane RAM, not defrag, not disk

Full write-up (provider asks + the talosctl quick-ref) ↓

<link>

#Kubernetes #Talos #TrueNAS #Homelab
```

**Notes**:
- 8 posts is at the upper end — if you need to cut, merge 5+7 (the cascade ASCII into the fix list); the diagnostic path (1–4) is the load-bearing arc and shouldn't be compressed.
- The diagram in post 5 is text-arrow flow, not ASCII art. Acceptable per the no-ASCII-diagrams rule because it's a vertical causal chain, not a box-and-line drawing. If anyone pushes back, convert to a Mermaid diagram in the dev.to post and skip the X post entirely.

---

## X Communities — variants

Same hook, retuned per Community per the rules in `_shared/x-communities.md`. Post one Community per day, leave 24h between drops. Skip any Community that has rules against links.

### Self-Hosted (Tier 1)

```
Story from my own homelab cluster this week:

Single-control-plane Talos VM, 12 GB RAM, no swap. Looked fine on
day one. Walked into memory exhaustion over ~4.5 days of uptime
once Crossplane + ArgoCD + ESO + Longhorn settled in.

Result: cluster-wide control-plane brownout. Deployments stopped
rolling. scheduler/cm flapping. etcd health pegging between OK
and "context deadline exceeded" every minute.

The trap: it looks exactly like an etcd defrag problem. It isn't.
The etcd DB was 135 MB.

The other trap: instinct says "add swap." Talos has none, by
design. And on an etcd node it would be strictly worse anyway —
you'd convert OOMs into permanent fsync latency.

Real fix: more RAM, ballooning off, get off single-CP.

Full write-up (and a quick talosctl cheat-sheet for diagnosing
this) <link>

#Homelab #SelfHosted #Kubernetes #TrueNAS
```

### Homelab (Tier 1)

```
"It was working fine and then everything broke at once" —
a homelab Kubernetes story.

12 GB control-plane Talos VM on a TrueNAS host. Four days of
uptime later: 90+ scheduler crashloops, flapping etcd,
Deployments stuck observedGeneration < generation.

Tempting reads: etcd bloat (it wasn't — 135 MB), bad disk
(it wasn't), need swap (Talos has none, and you wouldn't want
it on etcd anyway).

Actual cause: under-sized CP RAM. Once MemAvailable dropped low
enough, Talos's OOM controller started SIGKILLing best-effort
cgroups in a loop, reclaim stalls broke etcd's fsync budget,
and the whole control plane cascaded.

Diagnostic + fix + provider asks: <link>

#Homelab #Kubernetes #Talos #TrueNAS
```

### Kubernetes (Tier 1)

```
Pattern worth knowing: a single under-sized control-plane node
can produce a full control-plane brownout that looks like
every K8s bug at once.

Real example from a Talos + Omni cluster this week:

→ Deployment changes applied, no pod ever appeared
   (observedGeneration < generation, frozen)
→ kube-scheduler CrashLoopBackOff 93x — dies at
   "waiting for caches to sync"
→ kube-controller-manager flapping 83x
→ etcd health oscillating OK ↔ context deadline every ~60s
→ Underneath all of it: MemAvailable ~1.4 GiB of ~12 GiB,
   OOM controller killing best-effort cgroups (kube-proxy)
   in a loop

The diagnostic tell: small healthy etcd DB + low MemAvailable.
If you only look at etcd you'll defrag a node that doesn't
need it and the symptoms will persist.

Full path + the no-swap reasoning: <link>

#Kubernetes #Talos #CloudNative
```

### Build in Public (Tier 1)

```
Field report from running my own open-source infra in
production: a CP-sizing default I chose months ago bit me.

12 GB Talos control-plane VM, single CP, ~4.5 days uptime,
no swap (Talos has none). Cluster browned out cluster-wide.
Took me an hour of suspecting etcd defrag before I realized
the etcd DB was 135 MB and the actual problem was RAM
underneath it.

Writing it up as much for the provider asks as the story:
raise the CP memory floor, surface no-swap in the docs,
default control-plane VMs to fixed (non-ballooned) memory.

Build-in-public means writing the post even when the bug is
in your own defaults.

<link>

#BuildInPublic #OpenSource #Kubernetes
```

### DevOps (Tier 2 — only post if you have a fresh angle)

```
Production cascade pattern, control-plane edition:

mem pressure on CP node
  → Talos userspace OOM controller kills best-effort cgroups in loop
  → reclaim stalls
  → etcd misses fsync deadlines
  → flapping etcd
  → slow apiserver
  → scheduler/controller-manager can't sync caches or hold leases
  → cluster-wide scheduling + rollout freeze

The visible failure (CrashLoopBackOff scheduler, frozen Deployments)
is five hops downstream of the actual cause (under-sized CP RAM).

The diagnostic shortcut that would have saved me an hour:

  talosctl -n $N read /proc/meminfo | grep -E 'Available|Swap'

If MemAvailable is low and SwapTotal is 0 (Talos: always), stop
debugging etcd, go add RAM.

<link>

#DevOps #Kubernetes #SRE
```

---

## Posting order

| Day | Channel | Notes |
|---|---|---|
| Tue/Wed AM | LinkedIn long-form | Reply for first 4 hours |
| Same day +2h | X main thread | After LinkedIn engagement settles |
| Day +1 | X Community: Self-Hosted | Smallest first to tune hook |
| Day +2 | X Community: Homelab | Re-use Self-Hosted hook if engagement was strong |
| Day +3 | X Community: Kubernetes | Larger audience, technical framing |
| Day +5 | X Community: Build in Public | Maintainer-angle frame |
| Day +7 | X Community: DevOps | Only if you have appetite — Tier 2 |

Skip any community that has a no-self-link rule. Don't carpet-post the same body.
