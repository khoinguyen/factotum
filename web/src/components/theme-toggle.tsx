import { useTheme } from "@/hooks/use-theme"
import type { Theme } from "@/lib/theme"
import { cn } from "@/lib/utils"

const options: { value: Theme; label: string }[] = [
  { value: "light", label: "Light" },
  { value: "dark", label: "Dark" },
  { value: "system", label: "System" },
]

// ThemeToggle is a three-way segmented control: an explicit Light/Dark choice or
// System to follow the OS. It is the only place the theme is set.
export function ThemeToggle() {
  const { theme, setTheme } = useTheme()

  return (
    <div
      role="group"
      aria-label="Theme"
      data-slot="theme-toggle"
      className="flex shrink-0 items-center gap-0.5 rounded-md border p-0.5"
    >
      {options.map((option) => {
        const active = theme === option.value
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={active}
            onClick={() => setTheme(option.value)}
            className={cn(
              "rounded-sm px-2 py-0.5 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground",
              active && "bg-accent text-accent-foreground",
            )}
          >
            {option.label}
          </button>
        )
      })}
    </div>
  )
}
