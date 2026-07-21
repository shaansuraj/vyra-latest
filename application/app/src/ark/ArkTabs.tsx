import { Tabs } from '@ark-ui/solid';
import { createMemo, createSignal, JSX, Setter } from 'solid-js';

export interface TabItem {
  value: string;
  label: string;
  content: JSX.Element;
  icon?: JSX.Element;
}

export const ArkTabs = (props: {
  items: TabItem[];
  defaultValue?: string;
  value?: string;
  onValueChange?: Setter<string>;
  listClass?: string;
  indicatorPosition?: 'top' | 'bottom';
}) => {
  const position = props.indicatorPosition || 'bottom';
  const [internalValue, setInternalValue] = createSignal(props.defaultValue || props.items[0]?.value || '');
  const activeValue = createMemo(() => props.value ?? internalValue());

  return (
    <Tabs.Root
      value={activeValue()}
      onValueChange={(details) => {
        if (props.value === undefined) setInternalValue(details.value);
        props.onValueChange?.(details.value);
      }}
      class="h-full flex flex-col"
    >
      <Tabs.List class={`flex items-center gap-1 p-1.5 rounded-2xl bg-neu-900/72 border border-neu-800/80 relative ${props.listClass || ''}`}>
        {props.items.map((item) => (
          <Tabs.Trigger
            value={item.value}
            class="group px-4 py-2.5 text-sm font-medium text-neu-400 hover:text-neu-100 transition-all duration-200 border border-transparent data-[selected]:text-neu-50 data-[selected]:bg-neu-800/90 data-[selected]:border-neu-700/90 rounded-xl flex items-center gap-2 lift-on-hover"
          >
            <span class="icon-pop">{item.icon}</span>
            {item.label}
          </Tabs.Trigger>
        ))}
        <Tabs.Indicator
          class="rounded-full bg-gradient-to-r from-cyan-400/90 to-violet-400/80"
          style={{
            height: '2px',
            width: 'var(--width)',
            ...(position === 'bottom' ? { bottom: '0' } : { top: '0' }),
          }}
        />
      </Tabs.List>

      {props.items.map((item) => (
        <Tabs.Content value={item.value} class="flex-1 min-h-0 overflow-auto focus:outline-none kinetic-entrance data-[state=closed]:hidden">
          {activeValue() === item.value ? item.content : null}
        </Tabs.Content>
      ))}
    </Tabs.Root>
  );
};
