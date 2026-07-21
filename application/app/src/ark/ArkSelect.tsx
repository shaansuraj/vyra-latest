import { Select, createListCollection } from '@ark-ui/solid';
import { FiCheck, FiChevronDown } from 'solid-icons/fi';
import { For, createMemo } from 'solid-js';
import { Portal } from 'solid-js/web';

export type SelectItem = {
  label: string;
  value: string;
};

export const ArkSelect = (props: {
  items: SelectItem[];
  value: () => string;
  onValueChange?: (details: { value: string[] }) => void;
  onChange?: (value: string) => void;
  placeholder?: string;
  positioning?: {
    sameWidth?: boolean;
  };
  disabled?: boolean;
}) => {
  const collection = createMemo(() =>
    createListCollection({
      items: props.items,
    })
  );

  return (
    <Select.Root
      collection={collection()}
      value={[props.value()]}
      onValueChange={(details) => {
        props.onValueChange?.(details);
        props.onChange?.(details.value?.[0] || '');
      }}
      positioning={props.positioning}
      disabled={props.disabled}
    >
      <Select.Control class="relative">
        <Select.Trigger class="px-2.5 py-1.5 text-xs font-medium text-neu-300 hover:text-neu-100 bg-neu-900 rounded-lg hover:bg-neu-850 border border-neu-750 focus:outline-none flex items-center justify-between gap-1 transition-all duration-150 min-w-32 data-[disabled]:opacity-50 data-[disabled]:cursor-not-allowed">
          <Select.ValueText placeholder={props.placeholder || 'Select...'} />
          <Select.Indicator class="flex items-center shrink-0">
            <FiChevronDown class="w-4 h-4" />
          </Select.Indicator>
        </Select.Trigger>
      </Select.Control>
      <Portal>
        <Select.Positioner>
          <Select.Content class="bg-neu-900 border border-neu-700 rounded-lg shadow-lg shadow-black/35 z-50 max-h-[300px] overflow-auto">
            <For each={collection().items}>
              {(item) => (
                <Select.Item item={item} class="flex items-center justify-between px-3 py-2 cursor-pointer hover:bg-neu-800 transition-colors data-highlighted:bg-neu-800 text-neu-200 text-sm">
                  <Select.ItemText>{item.label}</Select.ItemText>
                  <Select.ItemIndicator>
                    <FiCheck class="w-4 h-4 text-cyan-300" />
                  </Select.ItemIndicator>
                </Select.Item>
              )}
            </For>
          </Select.Content>
        </Select.Positioner>
      </Portal>
    </Select.Root>
  );
};
