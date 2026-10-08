# Solistrom gateway

Solistrom supports a lot of solar hardware out of the box, but not everything.
This is a small program that fills the gap: it reads a device on your own
network and sends its readings to Solistrom every few seconds, so the device
shows up in the app like a supported one.

It currently reads two kinds of hardware:

- Growatt inverters through an
  [OpenInverterGateway](https://github.com/OpenInverterGateway/OpenInverterGateway)
  datalogger.
- Hoymiles microinverters through an [AhoyDTU](https://docs.ahoydtu.de)
  datalogger.
- APsystems EZ1 microinverters through the local API built into the inverter.

Note that AhoyDTU and OpenDTU are different firmware for the same Hoymiles
inverters. This program speaks to AhoyDTU; OpenDTU is not supported yet.

## What you need

- One of the supported setups, reachable on your network:
  - a Growatt inverter with OpenInverterGateway, where you should be able to open
    `http://<its address>/status` in a browser and see a page full of numbers;
  - or a Hoymiles inverter with AhoyDTU, where `http://<its address>/api/index`
    should show you a short page listing your inverters;
  - or an APsystems EZ1 with its local API switched on, where you open
    `http://<its address>:8050/getOutputData` and you should see a line of
    numbers.
- A Solistrom account.
- Go 1.21 or newer to build the program.

## Getting a push URL

In the Solistrom app, add a generic push device and generate an API key. The
app shows you a URL. Copy the whole thing.

Treat that URL like a password. Anyone who has it can write readings into your
account, so do not paste it into a chat, a screenshot, or a file you share.

## Setting it up

Copy the example configuration and open it in an editor:

    cp config.example.json config.json

Fill in two things: `url` is your inverter's address, and `push_url` is the URL
you copied from the app.

    {
      "log_level": "info",
      "devices": [
        {
          "name": "growatt-pv",
          "type": "growatt-openinvertergateway",
          "connection": "http",
          "url": "http://10.0.0.240",
          "push_url": "https://push.example.com/...",
          "interval": "10s",
          "timeout": "5s",
          "battery": "auto",
          "report_grid": false,
          "retry": { "attempts": 3, "backoff": "2s" }
        }
      ]
    }

`config.json` is ignored by git, so your key will not end up in a commit by
accident. If you would rather keep it out of the file entirely, leave
`push_url` empty and set the environment variable
`SOLISTROM_PUSH_URL_GROWATT_PV` instead. The name is your device name in
capitals, with anything that is not a letter or digit replaced by an
underscore.

To add a second device, add another entry to the list. Each one needs its own
name and its own push URL from the app.

## Running it

    go build ./cmd/solistromgateway
    ./solistromgateway

To check which build you have, which is the first thing to include in a bug
report:

    ./solistromgateway version

It prints the version, the exact revision it was built from, and whether the
working tree had uncommitted changes at the time. `-version` does the same
thing. Either works even when your configuration file is broken, which is
usually when you want it.

The same version and revision appear on the first line of the log every time
the program starts.

It reads the first value straight away and then keeps going every ten seconds.
Press Ctrl-C to stop. It does not wait around: if a send is in progress it is
cut short rather than left to finish, and the program logs `stopped` once
everything has wound down.

To keep it running, the terminal window it is running in has to stay open. If
you want it to keep going after you close the terminal, or after you log out,
run it with something designed for that: `nohup ./solistromgateway &` is the
simplest option, or set it up as a `launchd` job on a Mac or a `systemd`
service on Linux. That kind of packaging is outside the scope of this project,
so look up whichever of those fits your system.

## Running it in Docker

There is a `Dockerfile` and a `docker-compose.yml` if you would rather not keep
a terminal open. The image contains the program and a bundle of certificates and
nothing else at all: no shell, no package manager, no libc. It runs as `nobody`
with no capabilities and a read-only filesystem.

    export VERSION="$(git describe --tags --always --dirty)"
    docker compose up -d --build

Your `config.json` is mounted in read-only and is never built into the image.
Because the container runs as uid 65534, that file has to be readable by others:
`0644` is fine, or `chown` it to `65534` and keep it at `0600`.

Two things to know. The container has no clock setting of its own, so timestamps
in the log are UTC rather than your local time. And there is no health check,
because a container with no shell has nothing to run one with. Use
`docker compose logs` to see how it is getting on.

## The settings explained

- `log_level`: how much the program writes to its log. This sits at the top
  of the file, alongside `devices`, not inside one device. The default,
  `info`, only shows you the start-up line and anything that goes wrong, so
  after that you will see nothing at all for as long as things keep working.
  A successful send is only logged at the `debug` level. When you first set
  this up, set `log_level` to `"debug"` and watch the log for a few minutes to
  see pushes actually succeeding; switch it back to `"info"` once you are
  confident it is working, so the log does not fill up with a line every few
  seconds.
- `name`: whatever you want to call the device. It appears in the log lines,
  so pick something you will recognise.
- `type`: which kind of device this is. One of
  `growatt-openinvertergateway`, `hoymiles-ahoydtu` or `apsystems-local`.
- `connection`: how the program talks to the device. `http` is the only
  value supported right now.
- `inverter_id`: AhoyDTU only. Which inverter on the datalogger this entry
  reads, counting from 0. You can leave it out if you only have one. See the
  AhoyDTU section below.
- `url`: the address of the device on your network, with no path on the end.
  The program knows which path each kind of device uses and adds it itself.
  For an APsystems EZ1 include the port, as in `http://10.0.0.207:8050`.
- `username` and `password`: only for a Growatt gateway that asks for a login,
  and most do not. AhoyDTU and APsystems do not use this kind of login, so
  leave these empty for them.
- `interval`: how often to send. Solistrom asks for every 5 to 10 seconds, and
  the program will not accept anything faster than 5 seconds.
- `timeout`: how long to wait for a slow answer. Must be shorter than the
  interval.
- `battery`: `auto` works out whether you have a battery by looking at the
  reported voltage. Set it to `off` to never send battery values, or `on` if
  you have a battery that the automatic check misses.
- `report_grid`: off by default, and you probably want to leave it that way.
  See the note below.
- `retry`: how many times to try a send in total if it keeps failing (so
  `attempts: 3` means three tries altogether, not three retries after the
  first), and how long to wait before trying again. Each retry waits twice as
  long as the one before. Retries also stop early if there is no time left
  before the next scheduled send: with the defaults shown above (a 5 second
  timeout, a 2 second backoff, and a 10 second interval), the third attempt
  often never gets a chance to happen.

## If you have a Hoymiles inverter and AhoyDTU

One AhoyDTU datalogger can serve up to four inverters, and in Solistrom each
inverter is its own device with its own push URL. So you add one entry per
inverter, all pointing at the same datalogger address, each with its own
`inverter_id` and its own `push_url`:

    {
      "devices": [
        {
          "name": "roof",
          "type": "hoymiles-ahoydtu",
          "url": "http://10.0.0.197",
          "inverter_id": 0,
          "push_url": "https://push.example.com/...",
          "interval": "15s"
        },
        {
          "name": "garage",
          "type": "hoymiles-ahoydtu",
          "url": "http://10.0.0.197",
          "inverter_id": 1,
          "push_url": "https://push.example.com/...",
          "interval": "15s"
        }
      ]
    }

With a single inverter you can leave `inverter_id` out entirely; it means the
first one. Open `http://<your datalogger>/api/index` in a browser to see which
ids it has and what they are called.

If two entries end up reading the same inverter, the program refuses to start
and tells you which two. That is deliberate: Solistrom would count that
inverter's production twice and your app would show roughly double what you
actually produce.

AhoyDTU asks its inverters for new figures every 15 seconds or so, so there is
little point polling faster than that. `"interval": "15s"` matches the hardware;
anything quicker just sends the same number again.

Unlike the Growatt setup, the AhoyDTU box has its own power supply and keeps
answering after dark. It simply reports the inverter as unavailable. The program
treats that as "no reading right now" rather than as zero production, because
the radio link to a microinverter can also drop in broad daylight, and it has no
way to tell those two apart.

## If you have an APsystems EZ1

Switch on the local API in the APsystems app first, as it is off by default. Once
it is on, the inverter answers on port 8050, and the port goes in the address:

    {
      "name": "balcony",
      "type": "apsystems-local",
      "url": "http://10.0.0.207:8050",
      "push_url": "https://push.example.com/...",
      "interval": "10s"
    }

An EZ1 is one inverter with two panel inputs. The program adds both inputs
together and sends the total, so there is nothing to choose and no
`inverter_id` here.

The inverter is powered by the panels, so it disappears from the network after
dark, the same as the Growatt setup. That is normal, and the log says so once
rather than every ten seconds.

One small thing worth knowing: this inverter puts its serial number in every
answer it gives. The program never sends it anywhere and strips it out of log
messages, so you can paste a log line into a bug report without handing over
your serial.

## A note about grid readings

Solistrom's main reading is grid power: how much you are drawing from or
feeding into the grid. Most inverters cannot measure that. They only know what
they are producing.

So by default this program sends production only, and lets Solistrom take your
grid figure from the meter you set up in the app. That is almost always what
you want, and it is why `report_grid` starts switched off.

If your inverter does have a meter attached, set `report_grid` to `true` and it
will send grid power as well. This applies to the Growatt setup only. The
Hoymiles and APsystems microinverters have no meter and no battery, so
`report_grid` and `battery` do nothing there, and the program says so at startup
if you set them.

Turning it on without a meter will not put a wrong number into your account.
Growatt inverters report the meter readings as zero whether or not a meter is
wired in, so the program looks at the lifetime totals instead: if nothing has
ever flowed through the meter, there is no meter, and it leaves grid power out
rather than claiming your house is perfectly balanced. You will see a line in
the log saying so. Some inverters have no grid readings at all, and you get a
warning for that too.

Either way, if you know there is no meter, leave the setting off and save
yourself the log noise.

## What it tells you when it starts

The first thing it does is check every device in your configuration and say what
it found. When everything is there:

    level=INFO msg=starting version=v1.0.0 go=go1.27.1 platform=linux/arm64 cpus=4 devices=3
    level=INFO msg="all devices answered" reachable=3

When something is not, it names it and says why:

    level=WARN msg="some devices could not be reached" reachable=2 unreachable=1 not_answering=roof
    level=WARN msg="device did not answer the startup check" device=roof error="no inverter with id 3; this datalogger has 0 (Flex)"

A device that cannot be reached never stops the program. After dark none of them
answer, and it has to keep running through that.

There is a third thing it may say, which is not a fault:

    level=INFO msg="devices answered but had nothing to report yet" devices=roof

That means the device replied perfectly well but has no figure for you at the
moment, such as an AhoyDTU that has lost contact with one of its inverters. It
is reported separately from being unreachable because the two have different
causes.

The check asks all the devices at once, so it takes about as long as your
slowest `timeout` rather than the sum of them.

## Keeping the push URL to yourself

The push URL is the one secret here. Anyone who has it can write readings into
your account, so the program tries not to let it escape.

It is never written to a log: log lines show only the scheme and host, so
pasting one into a bug report is safe. It is never accepted on the command line,
where it would show up in a process list. And on startup the program asks the
operating system to protect its own memory, which you will see as:

    level=INFO msg="memory protection in place" measures=2

On Linux that means two things: core dumps are switched off, so a crash cannot
write your push URL into a file, and the process is marked as not dumpable, so
another program running as the same user cannot read its memory or its
environment. On macOS only the core dump part applies, and it reports one
measure instead of two. Neither needs any special privileges, so both still work
inside the locked-down container.

If you ever need to attach a debugger, `-allow-debug` turns this off. It says so
loudly in the log when you do.

Two things left to you. Keep `config.json` at `chmod 600`; the program tells you
if it is wider. And prefer the file over the `SOLISTROM_PUSH_URL_` environment
variable: the environment of a running process is more exposed than a file with
tight permissions, and nothing the program can do will scrub it.

## Overnight, and other quiet spells

Your inverter shuts down when the sun goes, and the small gateway attached to it
loses power along with it. So from dusk until morning the program cannot read
anything. That is normal and nothing is broken.

Rather than repeating itself every ten seconds all night, it writes one line
when the device stops answering, reminds you every half hour while it is still
quiet, and writes one more line when the device comes back:

    device is answering again failures=4644 down_for=12h54m0s

The same applies to any other long spell of trouble, such as a network outage or
Solistrom being unreachable. If you want to see every single attempt, set
`log_level` to `debug` and they are all still there.

## When something is wrong

The program keeps running when a device goes quiet. It writes a line and tries
again at the next interval, so a reboot or a brief network drop sorts itself
out. Several devices run independently; one failing does not stop the others.

Some lines you might see:

- `could not read device, skipping this push`: the inverter did not answer, or
  answered with something unreadable. Check the address, and try opening
  `/status` in a browser. The program will not send a stale reading in place of
  a missing one.
- `unrecognised /status response`: the program did not find a power reading it
  knows. The line lists the field names your device returned; open an issue
  with that list.
- `returned 401 Unauthorized`: your gateway wants a login. Fill in `username`
  and `password`.
- `giving up on this push until the next interval`: Solistrom did not accept
  the reading, and the program has used up its attempts for this round. It will
  try again at the next interval. If it keeps happening, check that the push URL
  is still valid in the app. Each individual attempt is logged at `debug` as
  `push attempt failed`, if you want the detail.

Push URLs are always shortened in the log, so your key does not end up in a log
file.

## Running the tests

    go test ./...

The tests do not touch the network or your inverter.
