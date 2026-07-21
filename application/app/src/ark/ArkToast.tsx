import { Toast, Toaster, createToaster } from '@ark-ui/solid/toast';
import { BsXLg } from 'solid-icons/bs';
import { FaSolidCircleCheck, FaSolidCircleExclamation, FaSolidSpinner } from 'solid-icons/fa';

export default function ArkToast() {
  const getIcon = (type: string | undefined) => {
    switch (type) {
      case 'loading':
        return <FaSolidSpinner size={18} class="animate-spin text-cyan-300" />;
      case 'success':
        return <FaSolidCircleCheck size={18} class="text-green-300" />;
      case 'error':
        return <FaSolidCircleExclamation size={18} class="text-red-300" />;
      default:
        return null;
    }
  };

  return (
    <Toaster toaster={toaster}>
      {(toast) => (
        <Toast.Root>
          <div class="flex items-start space-x-3 p-4 bg-neu-900/96 backdrop-blur-md border border-neu-700/80 rounded-xl shadow-lg shadow-black/45 min-w-sm max-w-sm interaction-layer">
            {getIcon(toast().type)}
            <div class="flex-1 min-w-0">
              <Toast.Title class="text-sm font-semibold text-neu-100 truncate">{toast().title}</Toast.Title>
              <Toast.Description class="text-xs text-neu-400 mt-0.5">{toast().description}</Toast.Description>
            </div>
            <Toast.CloseTrigger class="text-neu-500 hover:text-neu-200 transition-colors icon-pop">
              <BsXLg />
            </Toast.CloseTrigger>
          </div>
        </Toast.Root>
      )}
    </Toaster>
  );
}

export const toaster = createToaster({
  overlap: true,
  placement: 'bottom-end',
  gap: 16,
});
