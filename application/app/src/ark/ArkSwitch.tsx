import { Switch } from '@ark-ui/solid';

export default function ArkSwitch(props: {
  checked: () => boolean;
  onCheckedChange: (details: { checked: boolean }) => void;
  label: string;
}) {
  return (
    <Switch.Root checked={props.checked()} onCheckedChange={props.onCheckedChange} class="flex items-center gap-3">
      <Switch.Control class="relative inline-flex h-6 w-11 items-center rounded-full border border-neu-700/80 bg-neu-800 transition-colors focus:outline-none data-[state=checked]:bg-cyan-600/85 data-[state=checked]:border-cyan-500/80 cursor-pointer">
        <Switch.Thumb class="inline-block h-5 w-5 translate-x-[1px] data-[state=checked]:translate-x-[21px] rounded-full bg-white shadow transition-transform duration-200" />
      </Switch.Control>
      <Switch.Label class="text-sm font-medium text-neu-300">{props.label}</Switch.Label>
      <Switch.HiddenInput />
    </Switch.Root>
  );
}
