# sinty-powerd

The power daemon of SintyOS. One process does what UPower,
power-profiles-daemon and thermald do on other systems: it publishes the
batteries and power supplies, switches the power profiles and keeps the
processor out of thermal throttling.

## Why it exists

The three daemons it replaces each kept their own view of the same power
supplies and CPU knobs, and each came with its own dependencies and start-up
order. On SintyOS, which does not boot with systemd, one daemon that owns the
power state is simpler to start, cheaper to run and easier to reason about.

Clients do not change. libupower-glib, `upower`, `powerprofilesctl` and the
desktop find the same names, objects and properties on the system bus.

## What it implements

- **`org.freedesktop.UPower`**: `EnumerateDevices`, `GetDisplayDevice`,
  `GetCriticalAction`, `EnumerateKbdBacklights`, the `DeviceAdded` and
  `DeviceRemoved` signals, and `OnBattery`, `LidIsPresent`, `LidIsClosed`.
  Every battery and line power supply is a `org.freedesktop.UPower.Device`
  with all of its properties, `Refresh`, `GetHistory` (charge, rate,
  time-full, time-empty, kept across restarts) and `EnableChargeThreshold`.
  The display device combines the system batteries. Values are computed the
  way UPower computes them: on a real laptop `upower -d` prints the same
  output against both.
- **Power profiles** on `org.freedesktop.UPower.PowerProfiles` and
  `net.hadess.PowerProfiles`: power-saver, balanced and performance applied to
  the ACPI platform profile and the energy performance preference of the CPU
  driver, `HoldProfile` and `ReleaseProfile` with holds dropped when their
  owner leaves the bus, and the chosen profile remembered across boots. On
  battery, balanced uses the `balance_power` preference unless
  `BatteryAware` is turned off, and below the low battery level a
  power-saver hold with the reason `Battery low` is taken until the computer
  is plugged in or the battery is back 5 points above that level.
- **Thermal guard.** When the processor package approaches its throttling
  temperature, its sustained RAPL power limit is lowered step by step, and
  raised again once it has cooled. While it is lowered, the power profiles
  report `PerformanceDegraded` as `high-operating-temperature`.
- **GPU switch** on `dev.sinty.Power1`: on laptops whose firmware can wire
  the panel to the dedicated GPU (ASUS `gpu_mux_mode`), `GpuMux` is the mode
  the firmware will boot with (`hybrid` or `dedicated`), `GpuMuxBooted` the
  mode it booted with, and `SetGpuMux` changes it for the next boot. Choosing
  `dedicated` is refused while the dedicated GPU has no driver, since the
  panel would stay dark. On other computers `GpuMux` is empty.
- **Reasons** on `dev.sinty.Power1`: what currently holds the computer
  back, as `[id, a, b]` entries. `thermal` gives the temperature in degrees
  and the share of power left, `hold-power-saver` and `hold-performance` the
  application and reason of a hold (the low battery hold comes from
  `sinty-powerd` itself), and `battery` means balanced prefers efficiency on
  battery.
- Power supply changes arrive as kernel uevents; the lid switch is read from
  its input device.

Changing the profile, the charge threshold or the GPU mode is allowed to root and to
regular users.

## Usage

    sinty-powerd [--root /] [--state /var/lib/sinty-powerd]

## Build

    make build            # ./sinty-powerd
    make static           # stripped static binary for the image
    make test
    go build ./...        # plain build

Requires Go >= 1.25 and Linux. The non-stdlib dependency is
`github.com/godbus/dbus/v5`.
