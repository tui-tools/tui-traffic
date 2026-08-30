# Fixtures

Every parser in this package is tested against output captured from a real
machine, because a parser tested only against what its author imagined is a
parser that works on one machine. What is in here, and where it came from:

| File | Source |
| --- | --- |
| `proc-net-dev.txt` | `/proc/net/dev`, a Fedora 42 workstation running containers and a VPN |
| `proc-net-dev-overflow.txt` | Written by hand: the two shapes the kernel's fixed-width columns produce on a long-running machine — a name run together with its first counter, and a name wider than its column |
| `proc-net-tcp.txt`, `proc-net-tcp6.txt`, `proc-net-udp.txt`, `proc-net-udp6.txt` | The four socket tables of the same machine |
| `conntrack-extended.txt` | `conntrack -L -o extended`, written by hand |
| `conntrack-plain.txt` | `conntrack -L` without `-o extended`, which drops the address-family prefix |
| `conntrack-no-accounting.txt` | The same table on a kernel with `net.netfilter.nf_conntrack_acct=0`, which is the default nearly everywhere: no `packets=` and no `bytes=` at all |
| `proc-net-nf-conntrack.txt` | `/proc/net/nf_conntrack`, which is the same layout with a `zone=` column |

The conntrack fixtures are written rather than captured. The machine these
were collected on has no `conntrack` command and its `/proc/net/nf_conntrack`
is readable only by root, so there was nothing to capture; the rows follow the
format the kernel and conntrack-tools document, and the first machine in the
lab that has the real thing should replace them with a capture.

## Addresses

**Every address in this directory is from a documentation range**, and the
test suite enforces it: `TestFixturesCarryNoRealAddress` decodes each fixture
the way the parsers do and fails on anything that is not loopback, the
wildcard, or one of

- `192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24` (RFC 5737)
- `2001:db8::/32` (RFC 3849)

The captured files went through that replacement before they were committed:
the addresses were decoded from their hexadecimal columns, mapped one for one
into the documentation ranges, and re-encoded, so the layout is untouched and
the same address is still the same address across the four files. Loopback and
the wildcard were left alone — they name no machine.

The interface names in `proc-net-dev.txt` were replaced for the same reason: a
container bridge is named after a hash of something on the machine that made
it. The column widths were preserved, so the long-name row still exercises the
case where there is no space between the name and its first counter.

`TestFixturesCarryNoHostName` checks the other half of the promise on whatever
machine the suite runs on.

## Adding one

Paste the output that broke, scrub it the same way, and add a case to the
table test. A parser that is wrong on somebody's machine is fixed by making
their output the next fixture.
