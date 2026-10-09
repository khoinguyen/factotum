// RefreshBanner surfaces a failed read without taking over the page: the shell
// stays, the last good snapshot stays, and the error is a banner. stale marks a
// refresh that failed after a successful load, so the reader knows the data on
// screen is the last good one.
export function RefreshBanner({
  error,
  stale = false,
}: {
  error: string
  stale?: boolean
}) {
  return (
    <p
      role="alert"
      className="break-words rounded-md border border-amber-500/50 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-400"
    >
      {stale
        ? "Couldn't refresh — showing the last good snapshot. "
        : "Couldn't load the dashboard. "}
      {error}
    </p>
  )
}
