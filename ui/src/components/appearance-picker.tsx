import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select";

export function AppearancePicker() {
  const { appearanceReady, theme } = document.documentElement.dataset;
  // The small head script applies and persists the choice, before React loads.
  if (appearanceReady === undefined) return null;
  return (
    <label className="inline-flex shrink-0 flex-col items-start gap-1 text-sm font-medium text-muted-foreground min-[360px]:flex-row min-[360px]:items-center min-[360px]:gap-2">
      Appearance
      <NativeSelect
        className="min-h-11 min-w-24 text-foreground"
        data-appearance=""
        defaultValue={theme ?? "system"}
      >
        <NativeSelectOption value="system">System</NativeSelectOption>
        <NativeSelectOption value="light">Light</NativeSelectOption>
        <NativeSelectOption value="dark">Dark</NativeSelectOption>
      </NativeSelect>
    </label>
  );
}
