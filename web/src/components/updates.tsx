import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import type { UpdateView } from "@/lib/api"

export function Updates({ updates }: { updates: UpdateView[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Recent updates</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-1">
        {updates.length === 0 ? (
          <p className="text-sm text-muted-foreground">No events yet.</p>
        ) : (
          updates.map((update, i) => (
            <div key={`${update.time}-${i}`} className="flex gap-2 text-sm">
              <time className="shrink-0 font-mono text-xs text-muted-foreground">
                {update.time}
              </time>
              <span className="break-words">{update.summary}</span>
            </div>
          ))
        )}
      </CardContent>
    </Card>
  )
}
