import { type Component, type Accessor, type Setter, createSignal, Show } from 'solid-js';
import { Dialog } from '@ark-ui/solid/dialog';
import { Portal } from 'solid-js/web';
import MagneticButton from '../motion/MagneticButton';

export interface ContactFormModalProps {
  kind: Accessor<'sales' | 'demo'>;
  open: Accessor<boolean>;
  setOpen: Setter<boolean>;
  name: Accessor<string>;
  setName: Setter<string>;
  company: Accessor<string>;
  setCompany: Setter<string>;
  email: Accessor<string>;
  setEmail: Setter<string>;
  notes: Accessor<string>;
  setNotes: Setter<string>;
  onSubmit: (kind: 'sales' | 'demo') => void;
}

const ContactFormModal: Component<ContactFormModalProps> = (props) => {
  const [errors, setErrors] = createSignal<{ name?: boolean; email?: boolean }>({});
  const [submitted, setSubmitted] = createSignal(false);

  const title = () => (props.kind() === 'sales' ? 'Contact Sales' : 'Book a Demo');
  const subtitle = () =>
    props.kind() === 'sales'
      ? 'Tell us about your requirements and our team will reach out.'
      : 'Schedule a personalized walkthrough of the VYRA platform.';

  const validate = () => {
    const errs: { name?: boolean; email?: boolean } = {};
    if (!props.name().trim()) errs.name = true;
    if (!props.email().trim() || !props.email().includes('@')) errs.email = true;
    setErrors(errs);
    return Object.keys(errs).length === 0;
  };

  const handleSubmit = () => {
    if (!validate()) return;
    props.onSubmit(props.kind());
    setSubmitted(true);
    setTimeout(() => {
      setSubmitted(false);
      props.setOpen(false);
    }, 2000);
  };

  return (
    <Dialog.Root open={props.open()} onOpenChange={(d) => props.setOpen(d.open)}>
      <Portal>
        <Dialog.Backdrop class="v3-modal-backdrop" />
        <Dialog.Positioner class="v3-modal-positioner">
          <Dialog.Content class="v3-modal-content">
            <Show
              when={!submitted()}
              fallback={
                <div class="v3-modal-success">
                  <div class="v3-modal-success-icon">
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                      <path d="M20 6L9 17l-5-5" stroke-linecap="round" stroke-linejoin="round" />
                    </svg>
                  </div>
                  <h3>Message Sent</h3>
                  <p>Redirecting to WhatsApp...</p>
                </div>
              }
            >
              <div class="v3-modal-header">
                <div class="v3-modal-header-icon">
                  <Show
                    when={props.kind() === 'demo'}
                    fallback={
                      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                        <path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2" stroke-linecap="round" />
                        <circle cx="12" cy="7" r="4" stroke-linecap="round" />
                      </svg>
                    }
                  >
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5">
                      <rect x="2" y="3" width="20" height="14" rx="2" />
                      <path d="M8 21h8M12 17v4" stroke-linecap="round" />
                    </svg>
                  </Show>
                </div>
                <Dialog.Title class="v3-modal-title">{title()}</Dialog.Title>
                <Dialog.Description class="v3-modal-desc">{subtitle()}</Dialog.Description>
              </div>

              <div class="v3-modal-form">
                <div class="v3-modal-field-row">
                  <label class={`v3-modal-field ${errors().name ? 'is-error' : ''}`}>
                    <span>Name *</span>
                    <input
                      type="text"
                      value={props.name()}
                      onInput={(e) => {
                        props.setName(e.currentTarget.value);
                        if (errors().name) setErrors((prev) => ({ ...prev, name: false }));
                      }}
                      placeholder="Your full name"
                    />
                  </label>
                  <label class="v3-modal-field">
                    <span>Company</span>
                    <input
                      type="text"
                      value={props.company()}
                      onInput={(e) => props.setCompany(e.currentTarget.value)}
                      placeholder="Organization name"
                    />
                  </label>
                </div>
                <label class={`v3-modal-field ${errors().email ? 'is-error' : ''}`}>
                  <span>Email *</span>
                  <input
                    type="email"
                    value={props.email()}
                    onInput={(e) => {
                      props.setEmail(e.currentTarget.value);
                      if (errors().email) setErrors((prev) => ({ ...prev, email: false }));
                    }}
                    placeholder="name@company.com"
                  />
                </label>
                <label class="v3-modal-field">
                  <span>What are you looking for?</span>
                  <textarea
                    value={props.notes()}
                    onInput={(e) => props.setNotes(e.currentTarget.value)}
                    placeholder="Number of cameras, use case, timeline..."
                    rows={3}
                  />
                </label>
              </div>

              <div class="v3-modal-actions">
                <MagneticButton class="v3-btn v3-btn-primary v3-btn-lg" onClick={handleSubmit}>
                  {props.kind() === 'sales' ? 'Send to Sales' : 'Request Demo'}
                </MagneticButton>
                <button class="v3-modal-cancel" onClick={() => props.setOpen(false)}>
                  Cancel
                </button>
              </div>
            </Show>

            <Dialog.CloseTrigger class="v3-modal-close">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                <path d="M18 6L6 18M6 6l12 12" stroke-linecap="round" />
              </svg>
            </Dialog.CloseTrigger>
          </Dialog.Content>
        </Dialog.Positioner>
      </Portal>
    </Dialog.Root>
  );
};

export default ContactFormModal;
