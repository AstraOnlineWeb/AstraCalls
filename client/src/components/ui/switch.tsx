import { cn } from "@/lib/utils";

type SwitchProps = {
  checked: boolean;
  onCheckedChange: (value: boolean) => void;
  disabled?: boolean;
  "aria-label"?: string;
};

export const Switch = ({ checked, onCheckedChange, disabled, ...rest }: SwitchProps) => (
  <button
    type="button"
    role="switch"
    aria-checked={checked}
    disabled={disabled}
    onClick={() => onCheckedChange(!checked)}
    className={cn(
      "relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full transition-colors",
      "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 ring-offset-background",
      "disabled:cursor-not-allowed disabled:opacity-50",
      checked ? "bg-primary" : "bg-input",
    )}
    {...rest}
  >
    <span
      className={cn(
        "inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform",
        checked ? "translate-x-4" : "translate-x-0.5",
      )}
    />
  </button>
);
