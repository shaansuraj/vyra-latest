import { Dialog } from '@ark-ui/solid/dialog';
import { BsX } from 'solid-icons/bs';
import { createSignal, type JSX } from 'solid-js';
import { Portal } from 'solid-js/web';

export const ArkDialog = (props: {
  trigger: (open: boolean, setOpen: (open: boolean) => void) => JSX.Element;
  title: string;
  description?: string;
  children: JSX.Element | ((setOpen: (open: boolean) => void) => JSX.Element);
}) => {
  const [open, setOpen] = createSignal(false);

  return (
    <>
      {props.trigger(open(), setOpen)}
      <Dialog.Root open={open()} onOpenChange={(details) => setOpen(details.open)}>
        <Portal>
          <Dialog.Backdrop class="fixed inset-0 bg-black/72 backdrop-blur-md z-50" />
          <Dialog.Positioner class="fixed inset-0 flex items-center justify-center z-50 p-4">
            <Dialog.Content class="relative w-full max-w-xl max-h-[86vh] flex flex-col bg-neu-900/95 rounded-2xl border border-neu-700/70 shadow-2xl shadow-black/60">
              <div class="flex-shrink-0 px-6 pt-5 pb-3 border-b border-neu-800/70">
                <Dialog.Title class="m-0 text-base font-semibold text-white tracking-tight heading-display">{props.title}</Dialog.Title>
                {props.description && (
                  <Dialog.Description class="mt-1.5 text-xs leading-relaxed text-neu-500">{props.description}</Dialog.Description>
                )}
              </div>

              <div class="flex-1 overflow-y-auto px-6 pb-6 pt-4 min-h-0">
                {typeof props.children === 'function' ? props.children(setOpen) : props.children}
              </div>

              <Dialog.CloseTrigger class="absolute top-3 right-3 w-8 h-8 rounded-lg flex items-center justify-center text-neu-400 hover:text-white hover:bg-neu-800/80 focus:outline-none focus-visible:ring-2 focus-visible:ring-cyan-500 focus-visible:ring-offset-2 focus-visible:ring-offset-neu-800 icon-pop">
                <BsX class="w-6 h-6" />
              </Dialog.CloseTrigger>
            </Dialog.Content>
          </Dialog.Positioner>
        </Portal>
      </Dialog.Root>
    </>
  );
};
