package main

// Only the encrypted writer consumes these queues. Frames are droppable;
// reliable protocol messages always take priority before the next frame.
func (subscriber *subscriber) enqueueFrame(response backendResponse) {
	subscriber.frameMu.Lock()
	defer subscriber.frameMu.Unlock()
	select {
	case <-subscriber.done:
		return
	default:
	}
	select {
	case <-subscriber.frames:
	default:
	}
	subscriber.frames <- outboundResponse{response: response, generation: subscriber.generation.Load()}
}

func (subscriber *subscriber) clearFrame() {
	subscriber.frameMu.Lock()
	defer subscriber.frameMu.Unlock()
	select {
	case <-subscriber.frames:
	default:
	}
}

func (subscriber *subscriber) nextOutbound() (outboundResponse, bool) {
	for {
		select {
		case <-subscriber.done:
			return outboundResponse{}, false
		default:
		}
		select {
		case message := <-subscriber.responses:
			return message, true
		default:
		}
		select {
		case <-subscriber.done:
			return outboundResponse{}, false
		case message := <-subscriber.responses:
			return message, true
		case frame := <-subscriber.frames:
			select {
			case message := <-subscriber.responses:
				// Keep a more recent frame if the producer has already replaced it.
				subscriber.frameMu.Lock()
				select {
				case subscriber.frames <- frame:
				default:
				}
				subscriber.frameMu.Unlock()
				return message, true
			default:
				return frame, true
			}
		}
	}
}
